package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultPort      = "3000"
	defaultOnionLand = "https://www.onionland.to/search?q=%s&page=%d"
	defaultTorDex    = "http://tordexu73joywapk2txdr54jed4imqledpcvcuf75qsas2gwdgksvnyd.onion/search?query=%s&page=%d"
	defaultTorSocks  = "127.0.0.1:9050"
	defaultPageSize  = 15
	maxSearchPages   = 3
	maxProviderBytes = 4 << 20
	providerTimeout  = 20 * time.Second
	cacheTTL         = 2 * time.Minute
	maxQueryLength   = 120
)

type result struct {
	Title   string
	URL     string
	Snippet string
	Score   int
}

type cachedSearch struct {
	Created time.Time
	Results []result
}

type searchCache struct {
	mu      sync.RWMutex
	entries map[string]cachedSearch
}

var cache = searchCache{entries: make(map[string]cachedSearch)}

var (
	onionAnchorPattern = regexp.MustCompile(`(?is)<a\b[^>]*href\s*=\s*["']([^"']+)[^>]*>(.*?)</a>`)
	onionURLPattern    = regexp.MustCompile(`(?i)https?://(?:[a-z2-7]{16}|[a-z2-7]{56})\.onion(?:\:\d+)?(?:/[^\s"'<>)]*)?`)
	tagPattern         = regexp.MustCompile(`(?is)<[^>]+>`)
	spacePattern       = regexp.MustCompile(`\s+`)
	tokenPattern       = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}._-]{1,63}`)
)

func main() {
	http.HandleFunc("/", home)
	http.HandleFunc("/search", search)
	http.HandleFunc("/health", health)

	bind := os.Getenv("LIEN_BIND")
	if bind == "" {
		port := os.Getenv("PORT")
		if port == "" {
			port = defaultPort
		}
		bind = "127.0.0.1:" + port
	}

	server := &http.Server{
		Addr:              bind,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	log.Printf("Lien search engine listening on http://%s", bind)
	log.Fatal(server.ListenAndServe())
}

func home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile("index.html")
	if err != nil {
		http.Error(w, "Lien is unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

func search(w http.ResponseWriter, r *http.Request) {
	query := normalizeQuery(r.URL.Query().Get("q"))
	if query == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if len(query) > maxQueryLength {
		http.Error(w, "Search query is too long", http.StatusBadRequest)
		return
	}

	page := parsePage(r.URL.Query().Get("page"))
	results, err := searchIndex(r.Context(), query)
	if err != nil {
		http.Error(w, "Search failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	start := (page - 1) * defaultPageSize
	if start > len(results) {
		start = len(results)
	}
	end := start + defaultPageSize
	if end > len(results) {
		end = len(results)
	}
	writeResults(w, query, results[start:end], page, len(results))
}

func parsePage(value string) int {
	page, err := strconv.Atoi(value)
	if err != nil || page < 1 {
		return 1
	}
	if page > 20 {
		return 20
	}
	return page
}

func normalizeQuery(value string) string {
	value = strings.TrimSpace(value)
	return strings.Join(strings.Fields(value), " ")
}

func searchIndex(ctx context.Context, query string) ([]result, error) {
	key := strings.ToLower(query)
	if results, ok := cacheGet(key); ok {
		return results, nil
	}

	onionLandTemplate := os.Getenv("LIEN_ONIONLAND_URL")
	if onionLandTemplate == "" {
		onionLandTemplate = defaultOnionLand
	}

	torDexTemplate := os.Getenv("LIEN_TORDEX_URL")
	if torDexTemplate == "" {
		torDexTemplate = defaultTorDex
	}

	torSocks := os.Getenv("LIEN_TOR_SOCKS")
	if torSocks == "" {
		torSocks = defaultTorSocks
	}

	results, err := collectSearchProviders(ctx, query, onionLandTemplate, torDexTemplate, torSocks)
	if err != nil {
		return nil, err
	}
	results = rankResults(query, results)
	cachePut(key, results)
	return results, nil
}

type providerResult struct {
	name    string
	results []result
	err     error
}

func collectSearchProviders(ctx context.Context, query, onionLandTemplate, torDexTemplate, torSocks string) ([]result, error) {
	pages := make(chan providerResult, 2)
	var wg sync.WaitGroup

	providers := []struct {
		name   string
		tmpl   string
		useTor bool
	}{
		{name: "TorDex", tmpl: torDexTemplate, useTor: true},
		{name: "OnionLand", tmpl: onionLandTemplate, useTor: false},
	}

	for _, provider := range providers {
		provider := provider
		wg.Add(1)
		go func() {
			defer wg.Done()
			var results []result
			var err error
			for page := 1; page <= maxSearchPages; page++ {
				var pageResults []result
				if provider.useTor {
					pageResults, err = fetchTorProviderPage(ctx, provider.tmpl, query, page, torSocks)
				} else {
					pageResults, err = fetchHTTPProviderPage(ctx, provider.tmpl, query, page)
				}
				if err != nil {
					break
				}
				results = append(results, pageResults...)
			}
			pages <- providerResult{name: provider.name, results: results, err: err}
		}()
	}

	wg.Wait()
	close(pages)

	merged := make(map[string]result)
	var providerErrors []string
	for response := range pages {
		if response.err != nil {
			providerErrors = append(providerErrors, response.name+": "+response.err.Error())
		}
		for _, item := range response.results {
			key := canonicalResultKey(item.URL)
			if key == "" {
				continue
			}
			if existing, ok := merged[key]; ok {
				if len(item.Snippet) > len(existing.Snippet) {
					existing.Snippet = item.Snippet
				}
				if len(item.Title) > len(existing.Title) {
					existing.Title = item.Title
				}
				merged[key] = existing
				continue
			}
			merged[key] = item
		}
	}

	results := make([]result, 0, len(merged))
	for _, item := range merged {
		results = append(results, item)
	}
	if len(results) == 0 && len(providerErrors) == 2 {
		return nil, fmt.Errorf("all search providers failed: %s", strings.Join(providerErrors, "; "))
	}
	return results, nil
}

func fetchHTTPProviderPage(ctx context.Context, template, query string, page int) ([]result, error) {
	searchURL := fmt.Sprintf(template, url.QueryEscape(query), page)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Lien/2.1")
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("Accept-Language", "en-US,en;q=0.8")

	client := &http.Client{Timeout: providerTimeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("provider returned HTTP %d", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxProviderBytes))
	if err != nil {
		return nil, err
	}
	return parseProviderHTML(body), nil
}

func fetchTorProviderPage(ctx context.Context, template, query string, page int, socksAddr string) ([]result, error) {
	searchURL := fmt.Sprintf(template, url.QueryEscape(query), page)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Lien/2.1")
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("Accept-Language", "en-US,en;q=0.8")

	dialer := &socks5Dialer{address: socksAddr}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     false,
		ResponseHeaderTimeout: providerTimeout,
	}
	client := &http.Client{Transport: transport, Timeout: providerTimeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("TorDex returned HTTP %d", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxProviderBytes))
	if err != nil {
		return nil, err
	}
	return parseProviderHTML(body), nil
}

type socks5Dialer struct {
	address string
}

func (d *socks5Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", d.address)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(providerTimeout)); err != nil {
		conn.Close()
		return nil, err
	}
	if err := socks5Handshake(conn); err != nil {
		conn.Close()
		return nil, err
	}
	if err := socks5Connect(conn, address); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func socks5Handshake(conn net.Conn) error {
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if response[0] != 0x05 || response[1] != 0x00 {
		return fmt.Errorf("SOCKS5 proxy requires unsupported authentication")
	}
	return nil
}

func socks5Connect(conn net.Conn, address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid destination port")
	}

	request := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			request = append(request, 0x01)
			request = append(request, ip4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return fmt.Errorf("destination hostname too long")
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, []byte(host)...)
	}
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], uint16(port))
	request = append(request, portBytes[:]...)

	if _, err := conn.Write(request); err != nil {
		return err
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if response[0] != 0x05 || response[1] != 0x00 {
		return fmt.Errorf("SOCKS5 connect failed with code 0x%02x", response[1])
	}

	var skip int
	switch response[3] {
	case 0x01:
		skip = 4
	case 0x04:
		skip = 16
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return err
		}
		skip = int(length[0])
	default:
		return fmt.Errorf("unknown SOCKS5 address type")
	}
	if _, err := io.CopyN(io.Discard, conn, int64(skip+2)); err != nil {
		return err
	}
	return nil
}

func parseProviderHTML(body []byte) []result {
	seen := make(map[string]struct{})
	results := make([]result, 0)

	matches := onionAnchorPattern.FindAllSubmatch(body, -1)
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		target := normalizeOnionURL(string(match[1]))
		if target == "" {
			continue
		}
		title := cleanText(string(match[2]))
		if title == "" {
			title = target
		}
		if appendUniqueResult(&results, seen, target, title) {
			continue
		}
	}

	for _, raw := range onionURLPattern.FindAllString(string(body), -1) {
		target := normalizeOnionURL(raw)
		if target == "" {
			continue
		}
		appendUniqueResult(&results, seen, target, "Onion service")
	}

	return results
}

func appendUniqueResult(results *[]result, seen map[string]struct{}, target, title string) bool {
	key := canonicalResultKey(target)
	if key == "" {
		return false
	}
	if _, exists := seen[key]; exists {
		return false
	}
	seen[key] = struct{}{}
	*results = append(*results, result{Title: title, URL: target})
	return true
}

func normalizeOnionURL(raw string) string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if !strings.HasSuffix(host, ".onion") || !validOnionHost(host) {
		return ""
	}
	parsed.Fragment = ""
	return parsed.String()
}

func validOnionHost(host string) bool {
	label := strings.TrimSuffix(host, ".onion")
	if len(label) != 16 && len(label) != 56 {
		return false
	}
	for _, r := range label {
		if !(r >= 'a' && r <= 'z' || r >= '2' && r <= '7') {
			return false
		}
	}
	return true
}

func cleanText(value string) string {
	value = tagPattern.ReplaceAllString(value, " ")
	value = html.UnescapeString(value)
	value = spacePattern.ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}

func canonicalResultKey(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return strings.ToLower(raw)
	}
	return strings.ToLower(parsed.Host + parsed.EscapedPath())
}

func rankResults(query string, results []result) []result {
	terms := tokenPattern.FindAllString(strings.ToLower(query), -1)
	for i := range results {
		results[i].Score = scoreResult(terms, results[i])
		results[i].Snippet = makeSnippet(results[i].Snippet, results[i].Title, terms)
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return strings.ToLower(results[i].Title) < strings.ToLower(results[j].Title)
	})
	return results
}

func scoreResult(terms []string, item result) int {
	title := strings.ToLower(item.Title)
	address := strings.ToLower(item.URL)
	score := 0
	for _, term := range terms {
		if strings.Contains(title, term) {
			score += 12
		}
		if strings.Contains(address, term) {
			score += 4
		}
	}
	if strings.Contains(title, strings.ToLower(strings.Join(terms, " "))) {
		score += 8
	}
	return score
}

func makeSnippet(existing, title string, terms []string) string {
	base := cleanText(existing)
	if base == "" {
		base = cleanText(title)
	}
	if len(base) > 180 {
		base = base[:177] + "..."
	}
	return base
}

func cacheGet(key string) ([]result, bool) {
	cache.mu.RLock()
	entry, ok := cache.entries[key]
	cache.mu.RUnlock()
	if !ok || time.Since(entry.Created) > cacheTTL {
		return nil, false
	}
	return append([]result(nil), entry.Results...), true
}

func cachePut(key string, results []result) {
	cache.mu.Lock()
	cache.entries[key] = cachedSearch{Created: time.Now(), Results: append([]result(nil), results...)}
	if len(cache.entries) > 256 {
		for key := range cache.entries {
			delete(cache.entries, key)
			break
		}
	}
	cache.mu.Unlock()
}

func writeResults(w http.ResponseWriter, query string, results []result, page, total int) {
	escapedQuery := html.EscapeString(query)
	totalPages := (total + defaultPageSize - 1) / defaultPageSize
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")

	fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Lien - Search</title><style>`)
	fmt.Fprint(w, `body{margin:0;background:#111;color:#ddd;font:15px Arial,sans-serif}.wrap{max-width:900px;margin:auto;padding:20px}.top{display:flex;gap:10px;margin-bottom:18px}.top input{flex:1;min-width:0;padding:10px;border-radius:20px;border:0}.top button{padding:0 18px;border-radius:20px;border:0;background:#fff}.meta{color:#888;margin:8px 0 20px}.result{padding:15px 0;border-bottom:1px solid #2b2b2b}.result a{color:#9bc4ff;text-decoration:none;font-size:18px}.url{color:#6fc79b;font-size:13px;margin-top:4px;overflow-wrap:anywhere}.snippet{color:#aaa;margin-top:7px;line-height:1.45}.pages{display:flex;gap:12px;margin-top:20px}.pages a{color:#aaa;text-decoration:none}.empty{color:#888;padding:20px 0}.back{display:inline-block;margin-top:20px;color:#aaa}`)
	fmt.Fprint(w, `</style></head><body><main class="wrap"><form class="top" action="/search" method="get"><input name="q" value="`, escapedQuery, `" placeholder="Search..."><button type="submit">Search</button></form><div class="meta">Lien search · `, strconv.Itoa(total), ` result(s) for <strong>`, escapedQuery, `</strong></div>`)

	if len(results) == 0 {
		fmt.Fprint(w, `<div class="empty">No .onion results found.</div>`)
	} else {
		for _, item := range results {
			fmt.Fprint(w, `<article class="result"><a href="`, html.EscapeString(item.URL), `" target="_blank" rel="noreferrer noopener">`, html.EscapeString(item.Title), `</a><div class="url">`, html.EscapeString(item.URL), `</div><div class="snippet">`, html.EscapeString(item.Snippet), `</div></article>`)
		}
	}

	if totalPages > 1 {
		fmt.Fprint(w, `<nav class="pages">`)
		if page > 1 {
			fmt.Fprint(w, `<a href="/search?q=`, url.QueryEscape(query), `&page=`, strconv.Itoa(page-1), `">← Previous</a>`)
		}
		if page < totalPages {
			fmt.Fprint(w, `<a href="/search?q=`, url.QueryEscape(query), `&page=`, strconv.Itoa(page+1), `">Next →</a>`)
		}
		fmt.Fprint(w, `</nav>`)
	}
	fmt.Fprint(w, `<a class="back" href="/">← Back to Lien</a></main></body></html>`)
}
