package douban

import (
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pansou/config"
	"pansou/model"
	"pansou/plugin"
	"pansou/service"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/proxy"
)

const (
	pluginName         = "douban"
	pluginPriority     = 4
	defaultLimit       = 20
	maxLimit           = 100
	defaultFetchCount  = 100
	defaultCacheTTL    = 48 * time.Hour
	doubanAPIURLFormat = "https://m.douban.com/rexxar/api/v2/subject_collection/%s/items?start=0&count=%d"

	maxRequestRetries      = 3
	baseRetryBackoff       = 1200 * time.Millisecond
	aggressiveMinJitter    = 800 * time.Millisecond
	aggressiveJitterWindow = 2200 * time.Millisecond
	blockCooldown          = 10 * time.Minute
)

var rankingConfig = map[model.DoubanRankingType]struct {
	CollectionID string
	Name         string
}{
	model.DoubanRankingMovieHot: {
		CollectionID: "movie_hot_gaia",
		Name:         "电影热榜",
	},
	model.DoubanRankingTVAmerican: {
		CollectionID: "tv_american",
		Name:         "美剧热榜",
	},
	model.DoubanRankingTVHot: {
		CollectionID: "tv_domestic",
		Name:         "华语热榜",
	},
	model.DoubanRankingTVAnimation: {
		CollectionID: "tv_animation",
		Name:         "动漫热榜",
	},
	// 韩剧 tv_korean
	model.DoubanRankingTVKorean: {
		CollectionID: "tv_korean",
		Name:         "韩剧热榜",
	},
	// 日剧 tv_japanese
	model.DoubanRankingTVJapanese: {
		CollectionID: "tv_japanese",
		Name:         "日剧热榜",
	},
	model.DoubanRankingTVShow: {
		CollectionID: "tv_variety_show",
		Name:         "综艺热榜",
	},
	model.DoubanRankingNewMovies: {
		CollectionID: "movie_latest",
		Name:         "新片榜",
	},
}

var rankingOrder = []model.DoubanRankingType{
	model.DoubanRankingMovieHot,
	model.DoubanRankingTVHot,
	model.DoubanRankingTVAnimation,
	model.DoubanRankingTVShow,
	model.DoubanRankingNewMovies,
	model.DoubanRankingTVAmerican,
	model.DoubanRankingTVKorean,
	model.DoubanRankingTVJapanese,
}

type cachedRanking struct {
	Data      model.DoubanRankingResponse
	ExpiresAt time.Time
	FetchedAt time.Time
}

type DoubanPlugin struct {
	*plugin.BaseAsyncPlugin
	client *http.Client

	initMu      sync.Mutex
	initialized bool

	cacheMu  sync.RWMutex
	cache    map[model.DoubanRankingType]cachedRanking
	cacheTTL time.Duration
	interval time.Duration

	blockedUntilNano int64

	userAgents []string
	languages  []string
}

var _ plugin.AsyncSearchPlugin = (*DoubanPlugin)(nil)
var _ plugin.PluginWithWebHandler = (*DoubanPlugin)(nil)
var _ plugin.InitializablePlugin = (*DoubanPlugin)(nil)

func init() {
	plugin.RegisterGlobalPlugin(NewDoubanPlugin())
}

func NewDoubanPlugin() *DoubanPlugin {
	return &DoubanPlugin{
		BaseAsyncPlugin: plugin.NewBaseAsyncPluginWithFilter(pluginName, pluginPriority, true),
		client:          &http.Client{Timeout: 10 * time.Second},
		cache:           make(map[model.DoubanRankingType]cachedRanking),
		cacheTTL:        defaultCacheTTL,
		interval:        30 * time.Minute,
		userAgents: []string{
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 13_6_1) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:126.0) Gecko/20100101 Firefox/126.0",
			"Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1",
		},
		languages: []string{
			"zh-CN,zh;q=0.9,en;q=0.8",
			"zh-CN,zh;q=0.95,en-US;q=0.85,en;q=0.75",
			"en-US,en;q=0.9,zh-CN;q=0.7",
		},
	}
}

func (p *DoubanPlugin) Initialize() error {
	p.initMu.Lock()
	defer p.initMu.Unlock()

	if p.initialized {
		return nil
	}

	clientTimeout := 10 * time.Second
	proxyURL := ""
	if config.AppConfig != nil {
		clientTimeout = config.AppConfig.DoubanHTTPTimeout
		proxyURL = config.AppConfig.ProxyURL
		p.interval = config.AppConfig.DoubanFetchInterval
		p.cacheTTL = config.AppConfig.DoubanCacheTTL
	}
	p.client = newDoubanHTTPClient(clientTimeout, proxyURL)

	go func() {
		if err := p.refreshAllRankings(); err != nil {
			fmt.Printf("[Douban] 初次预抓取失败: %v\n", err)
		}
	}()
	go p.startPrefetchTask()

	p.initialized = true
	fmt.Printf("[Douban] 预抓取任务已启动，周期: %v\n", p.interval)
	return nil
}

func newDoubanHTTPClient(timeout time.Duration, proxyURL string) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	transport := &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		MaxConnsPerHost:       100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	if proxyURL != "" {
		parsedProxyURL, err := url.Parse(proxyURL)
		if err != nil {
			fmt.Printf("[Douban] 代理地址解析失败，使用直连: %v\n", err)
		} else if parsedProxyURL.Scheme == "socks5" {
			dialer, err := proxy.FromURL(parsedProxyURL, proxy.Direct)
			if err != nil {
				fmt.Printf("[Douban] SOCKS5代理初始化失败，使用直连: %v\n", err)
			} else {
				transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				}
				fmt.Printf("[Douban] 使用SOCKS5代理: %s\n", proxyURL)
			}
		} else {
			transport.Proxy = http.ProxyURL(parsedProxyURL)
			fmt.Printf("[Douban] 使用HTTP(S)代理: %s\n", proxyURL)
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}

func (p *DoubanPlugin) RegisterWebRoutes(router *gin.RouterGroup) {
	douban := router.Group("/api/douban")
	douban.GET("/rankings", p.handleRankings)
	fmt.Printf("[Douban] Web路由已注册: /api/douban/rankings\n")
}

func (p *DoubanPlugin) Search(keyword string, ext map[string]interface{}) ([]model.SearchResult, error) {
	rankings, err := p.GetAllRankings(defaultLimit)
	if err != nil {
		return nil, err
	}

	results := make([]model.SearchResult, 0)
	for _, ranking := range rankings {
		for _, item := range ranking.Items {
			results = append(results, model.SearchResult{
				UniqueID: fmt.Sprintf("douban:%s:%s", ranking.Type, item.SubjectID),
				Channel:  pluginName,
				Datetime: ranking.UpdatedAt,
				Title:    item.Title,
				Content:  fmt.Sprintf("%s 第%d名 评分%.1f", ranking.Name, item.Rank, item.Score),
				Links:    []model.Link{{Type: "others", URL: item.URL}},
				Tags:     []string{string(ranking.Type), ranking.Name},
			})
		}
	}

	if keyword == "" {
		return results, nil
	}
	return plugin.FilterResultsByKeyword(results, keyword), nil
}

func (p *DoubanPlugin) GetRanking(rankingType model.DoubanRankingType, limit int) (model.DoubanRankingResponse, error) {
	if _, ok := rankingConfig[rankingType]; !ok {
		return model.DoubanRankingResponse{}, fmt.Errorf("不支持的榜单类型: %s", rankingType)
	}

	limit = normalizeLimit(limit)
	if cached, ok := p.getCached(rankingType); ok {
		fmt.Printf("[Douban] 榜单 %s 命中内存缓存\n", rankingType)
		return trimRanking(cached, limit), nil
	}

	if cached, ok := p.getPersistedRanking(rankingType); ok {
		fmt.Printf("[Douban] 榜单 %s 命中磁盘缓存\n", rankingType)
		p.cacheMu.Lock()
		p.cache[rankingType] = cachedRanking{
			Data:      cached,
			FetchedAt: cached.UpdatedAt,
			ExpiresAt: cached.UpdatedAt.Add(p.cacheTTL),
		}
		p.cacheMu.Unlock()
		return trimRanking(cached, limit), nil
	}

	fresh, err := p.fetchAndStore(rankingType)
	if err != nil {
		if stale, ok := p.getStale(rankingType); ok {
			fmt.Printf("[Douban] 榜单 %s 回源失败，返回过期缓存: %v\n", rankingType, err)
			return trimRanking(stale, limit), nil
		}
		if stale, ok := p.getPersistedRanking(rankingType); ok {
			fmt.Printf("[Douban] 榜单 %s 回源失败，返回磁盘缓存兜底: %v\n", rankingType, err)
			return trimRanking(stale, limit), nil
		}
		return model.DoubanRankingResponse{}, err
	}
	fmt.Printf("[Douban] 榜单 %s 回源成功\n", rankingType)
	return trimRanking(fresh, limit), nil
}

func (p *DoubanPlugin) GetAllRankings(limit int) ([]model.DoubanRankingResponse, error) {
	start := time.Now()
	limit = normalizeLimit(limit)
	all := make([]model.DoubanRankingResponse, 0, len(rankingOrder))
	if p.isBlocked() {
		fmt.Printf("[Douban] 冷却窗口中，GetAllRankings 优先使用缓存\n")
	}
	for _, rankingType := range rankingOrder {
		ranking, err := p.GetRanking(rankingType, limit)
		if err != nil {
			fmt.Printf("[Douban] 获取榜单 %s 失败，已跳过: %v\n", rankingType, err)
			continue
		}
		all = append(all, ranking)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("所有榜单获取失败")
	}
	fmt.Printf("[Douban] GetAllRankings 完成，成功 %d/%d，耗时 %v\n", len(all), len(rankingOrder), time.Since(start))
	return all, nil
}

func (p *DoubanPlugin) handleRankings(c *gin.Context) {
	start := time.Now()
	typeParam := c.Query("type")
	limit := normalizeLimit(parseLimit(c.Query("limit")))
	fmt.Printf("[Douban] HTTP请求开始 type=%q limit=%d blocked=%v\n", typeParam, limit, p.isBlocked())
	defer func() {
		fmt.Printf("[Douban] HTTP请求结束 type=%q 耗时=%v\n", typeParam, time.Since(start))
	}()

	if typeParam == "" {
		rankings, err := p.GetAllRankings(limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(500, "获取豆瓣榜单失败: "+err.Error()))
			return
		}
		c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"rankings": rankings}))
		return
	}

	rankingType := model.DoubanRankingType(typeParam)
	ranking, err := p.GetRanking(rankingType, limit)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(400, err.Error()))
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(ranking))
}

func (p *DoubanPlugin) startPrefetchTask() {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for range ticker.C {
		if err := p.refreshAllRankings(); err != nil {
			fmt.Printf("[Douban] 定时预抓取失败: %v\n", err)
		}
	}
}

func (p *DoubanPlugin) refreshAllRankings() error {
	var firstErr error
	for _, rankingType := range rankingOrder {
		if _, err := p.fetchAndStore(rankingType); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			fmt.Printf("[Douban] 刷新 %s 失败: %v\n", rankingType, err)
		}
	}
	return firstErr
}

func (p *DoubanPlugin) fetchAndStore(rankingType model.DoubanRankingType) (model.DoubanRankingResponse, error) {
	start := time.Now()
	fresh, err := p.fetchRanking(rankingType, defaultFetchCount)
	if err != nil {
		return model.DoubanRankingResponse{}, err
	}

	now := time.Now()
	p.cacheMu.Lock()
	p.cache[rankingType] = cachedRanking{Data: fresh, FetchedAt: now, ExpiresAt: now.Add(p.cacheTTL)}
	p.cacheMu.Unlock()
	p.storePersistedRanking(fresh)
	fmt.Printf("[Douban] 榜单 %s 写入缓存完成，TTL=%v，耗时=%v\n", rankingType, p.cacheTTL, time.Since(start))
	return fresh, nil
}

func (p *DoubanPlugin) getCached(rankingType model.DoubanRankingType) (model.DoubanRankingResponse, bool) {
	p.cacheMu.RLock()
	defer p.cacheMu.RUnlock()

	cached, ok := p.cache[rankingType]
	if !ok || time.Now().After(cached.ExpiresAt) {
		return model.DoubanRankingResponse{}, false
	}
	return cached.Data, true
}

func (p *DoubanPlugin) getStale(rankingType model.DoubanRankingType) (model.DoubanRankingResponse, bool) {
	p.cacheMu.RLock()
	defer p.cacheMu.RUnlock()

	cached, ok := p.cache[rankingType]
	if !ok {
		return model.DoubanRankingResponse{}, false
	}
	return cached.Data, true
}

func (p *DoubanPlugin) fetchRanking(rankingType model.DoubanRankingType, count int) (model.DoubanRankingResponse, error) {
	cfg, ok := rankingConfig[rankingType]
	if !ok {
		return model.DoubanRankingResponse{}, fmt.Errorf("不支持的榜单类型: %s", rankingType)
	}
	if count <= 0 {
		count = defaultFetchCount
	}

	if p.isBlocked() {
		return model.DoubanRankingResponse{}, fmt.Errorf("豆瓣请求冷却中(剩余%s)", p.blockRemaining())
	}

	rawCacheKey := fmt.Sprintf("douban:raw:%s:%d", rankingType, count)
	if cached, ok := p.getRawCached(rawCacheKey); ok {
		fmt.Printf("[Douban] 榜单 %s 命中原始响应缓存 key=%s\n", rankingType, rawCacheKey)
		return p.buildRankingFromPayload(rankingType, cached)
	}
	fmt.Printf("[Douban] 榜单 %s 原始响应缓存未命中，开始回源\n", rankingType)

	apiURL := fmt.Sprintf(doubanAPIURLFormat, cfg.CollectionID, count)
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return model.DoubanRankingResponse{}, fmt.Errorf("创建请求失败: %w", err)
	}
	body, err := p.doRequestWithRetry(req)
	if err != nil {
		return model.DoubanRankingResponse{}, err
	}
	p.storeRawCache(rawCacheKey, body)

	ranking, err := p.buildRankingFromBody(rankingType, body)
	if err != nil {
		return model.DoubanRankingResponse{}, err
	}
	return ranking, nil
}

func (p *DoubanPlugin) buildRankingFromBody(rankingType model.DoubanRankingType, body []byte) (model.DoubanRankingResponse, error) {
	var payload doubanCollectionResp
	if err := json.Unmarshal(body, &payload); err != nil {
		return model.DoubanRankingResponse{}, fmt.Errorf("解析豆瓣响应失败: %w", err)
	}
	return p.buildRankingFromPayload(rankingType, payload)
}

func (p *DoubanPlugin) buildRankingFromPayload(rankingType model.DoubanRankingType, payload doubanCollectionResp) (model.DoubanRankingResponse, error) {
	cfg, ok := rankingConfig[rankingType]
	if !ok {
		return model.DoubanRankingResponse{}, fmt.Errorf("不支持的榜单类型: %s", rankingType)
	}

	items := make([]model.DoubanRankingItem, 0, len(payload.SubjectCollectionItems))
	for i, subject := range payload.SubjectCollectionItems {
		url := subject.URL
		if url == "" && subject.ID != "" {
			url = fmt.Sprintf("https://movie.douban.com/subject/%s/", subject.ID)
		}
		items = append(items, model.DoubanRankingItem{
			SubjectID: subject.ID,
			Title:     subject.Title,
			URL:       url,
			Rank:      i + 1,
			Score:     toFloat64(subject.Rating.Value),
		})
	}

	total := payload.Total
	if total <= 0 {
		total = len(items)
	}

	return model.DoubanRankingResponse{
		Type:      rankingType,
		Name:      cfg.Name,
		Total:     total,
		UpdatedAt: time.Now(),
		Items:     items,
	}, nil
}

func (p *DoubanPlugin) doRequestWithRetry(baseReq *http.Request) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < maxRequestRetries; attempt++ {
		if p.isBlocked() {
			return nil, fmt.Errorf("豆瓣请求冷却中(剩余%s)", p.blockRemaining())
		}

		fmt.Printf("[Douban] 请求尝试 attempt=%d/%d url=%s\n", attempt+1, maxRequestRetries, baseReq.URL.String())
		p.sleepWithJitter()

		req := baseReq.Clone(baseReq.Context())
		p.applyFingerprintHeaders(req)

		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("请求豆瓣接口失败: %w", err)
			fmt.Printf("[Douban] 请求失败 attempt=%d err=%v\n", attempt+1, lastErr)
			if attempt < maxRequestRetries-1 {
				p.sleepRetryBackoff(attempt)
				continue
			}
			break
		}

		respBytes, readErr := readResponseBody(resp)
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
			p.markBlocked()
			lastErr = fmt.Errorf("豆瓣接口限流/封禁状态: %d", resp.StatusCode)
			fmt.Printf("[Douban] 触发限流/封禁，进入冷却 %v，不再重试\n", blockCooldown)
			break
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("豆瓣接口返回状态异常: %d", resp.StatusCode)
			fmt.Printf("[Douban] 状态异常 attempt=%d status=%d\n", attempt+1, resp.StatusCode)
			if attempt < maxRequestRetries-1 {
				p.sleepRetryBackoff(attempt)
				continue
			}
			break
		}

		if readErr != nil {
			lastErr = fmt.Errorf("读取豆瓣响应失败: %w", readErr)
			fmt.Printf("[Douban] 读取失败 attempt=%d encoding=%q err=%v\n", attempt+1, resp.Header.Get("Content-Encoding"), readErr)
			if attempt < maxRequestRetries-1 {
				p.sleepRetryBackoff(attempt)
				continue
			}
			break
		}

		p.logResponsePreview(resp, respBytes, attempt)

		var payload doubanCollectionResp
		if err := json.Unmarshal(respBytes, &payload); err != nil {
			lastErr = fmt.Errorf("解析豆瓣响应失败: %w", err)
			fmt.Printf("[Douban] 解析失败 attempt=%d encoding=%q body=%dB err=%v\n", attempt+1, resp.Header.Get("Content-Encoding"), len(respBytes), err)
			if attempt < maxRequestRetries-1 {
				p.sleepRetryBackoff(attempt)
				continue
			}
			break
		}

		data, err := json.Marshal(payload)
		if err != nil {
			lastErr = fmt.Errorf("序列化豆瓣响应失败: %w", err)
			fmt.Printf("[Douban] 序列化失败 attempt=%d err=%v\n", attempt+1, err)
			if attempt < maxRequestRetries-1 {
				p.sleepRetryBackoff(attempt)
				continue
			}
			break
		}

		fmt.Printf("[Douban] 请求成功 attempt=%d bytes=%d\n", attempt+1, len(data))
		return data, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("请求豆瓣接口失败")
	}
	return nil, lastErr
}

func (p *DoubanPlugin) logResponsePreview(resp *http.Response, respBytes []byte, attempt int) {
	if os.Getenv("DOUBAN_DEBUG_BODY") != "1" {
		return
	}

	previewLen := 300
	if len(respBytes) < previewLen {
		previewLen = len(respBytes)
	}
	preview := string(respBytes[:previewLen])
	preview = strings.ReplaceAll(preview, "\n", "")
	preview = strings.ReplaceAll(preview, "\r", "")

	fmt.Printf("[Douban][DEBUG] attempt=%d status=%d encoding=%q bytes=%d preview=%q\n",
		attempt+1,
		resp.StatusCode,
		resp.Header.Get("Content-Encoding"),
		len(respBytes),
		preview,
	)
}

func (p *DoubanPlugin) sleepWithJitter() {
	extra := time.Duration(rand.Intn(int(aggressiveJitterWindow.Milliseconds()))) * time.Millisecond
	d := aggressiveMinJitter + extra
	fmt.Printf("[Douban] 随机抖动等待 %v\n", d)
	time.Sleep(d)
}

func (p *DoubanPlugin) sleepRetryBackoff(attempt int) {
	backoff := baseRetryBackoff * time.Duration(1<<attempt)
	extra := time.Duration(rand.Intn(600)) * time.Millisecond
	d := backoff + extra
	fmt.Printf("[Douban] 重试退避等待 %v (attempt=%d)\n", d, attempt+1)
	time.Sleep(d)
}

func (p *DoubanPlugin) applyFingerprintHeaders(req *http.Request) {
	ua := p.userAgents[rand.Intn(len(p.userAgents))]
	lang := p.languages[rand.Intn(len(p.languages))]

	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://m.douban.com/")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", lang)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-dest", "empty")
}

func (p *DoubanPlugin) markBlocked() {
	blockedUntil := time.Now().Add(blockCooldown).UnixNano()
	atomic.StoreInt64(&p.blockedUntilNano, blockedUntil)
	fmt.Printf("[Douban] 已进入冷却窗口，结束时间=%s\n", time.Unix(0, blockedUntil).Format(time.RFC3339))
}

func readResponseBody(resp *http.Response) ([]byte, error) {
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))

	var reader io.ReadCloser
	switch encoding {
	case "", "identity":
		reader = resp.Body
	case "gzip":
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip解压失败: %w", err)
		}
		defer gzipReader.Close()
		reader = gzipReader
	case "deflate":
		zlibReader, err := zlib.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("deflate解压失败: %w", err)
		}
		defer zlibReader.Close()
		reader = zlibReader
	case "br":
		return nil, fmt.Errorf("不支持br解压，请检查代理是否强制brotli")
	default:
		return nil, fmt.Errorf("不支持的Content-Encoding: %s", encoding)
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (p *DoubanPlugin) isBlocked() bool {
	blockedUntil := atomic.LoadInt64(&p.blockedUntilNano)
	if blockedUntil == 0 {
		return false
	}
	return time.Now().Before(time.Unix(0, blockedUntil))
}

func (p *DoubanPlugin) blockRemaining() time.Duration {
	blockedUntil := atomic.LoadInt64(&p.blockedUntilNano)
	if blockedUntil == 0 {
		return 0
	}
	remaining := time.Until(time.Unix(0, blockedUntil))
	if remaining < 0 {
		return 0
	}
	return remaining.Truncate(time.Second)
}

func (p *DoubanPlugin) persistedRankingCacheKey(rankingType model.DoubanRankingType) string {
	return fmt.Sprintf("douban:ranking:%s", rankingType)
}

func (p *DoubanPlugin) getRawCached(key string) (doubanCollectionResp, bool) {
	cache := service.GetEnhancedTwoLevelCache()
	if cache == nil {
		return doubanCollectionResp{}, false
	}

	data, hit, err := cache.Get(key)
	if err != nil || !hit {
		return doubanCollectionResp{}, false
	}

	var payload doubanCollectionResp
	if err := json.Unmarshal(data, &payload); err != nil {
		return doubanCollectionResp{}, false
	}
	return payload, true
}

func (p *DoubanPlugin) storeRawCache(key string, body []byte) {
	cache := service.GetEnhancedTwoLevelCache()
	if cache == nil {
		return
	}
	if err := cache.SetBothLevels(key, body, p.cacheTTL); err != nil {
		fmt.Printf("[Douban] 写入原始响应缓存失败: %v\n", err)
	}
}

func (p *DoubanPlugin) getPersistedRanking(rankingType model.DoubanRankingType) (model.DoubanRankingResponse, bool) {
	cache := service.GetEnhancedTwoLevelCache()
	if cache == nil {
		return model.DoubanRankingResponse{}, false
	}

	data, hit, err := cache.Get(p.persistedRankingCacheKey(rankingType))
	if err != nil || !hit {
		return model.DoubanRankingResponse{}, false
	}

	var ranking model.DoubanRankingResponse
	if err := json.Unmarshal(data, &ranking); err != nil {
		return model.DoubanRankingResponse{}, false
	}
	return ranking, true
}

func (p *DoubanPlugin) storePersistedRanking(ranking model.DoubanRankingResponse) {
	cache := service.GetEnhancedTwoLevelCache()
	if cache == nil {
		return
	}

	data, err := json.Marshal(ranking)
	if err != nil {
		fmt.Printf("[Douban] 序列化榜单缓存失败: %v\n", err)
		return
	}

	if err := cache.SetBothLevels(p.persistedRankingCacheKey(ranking.Type), data, p.cacheTTL); err != nil {
		fmt.Printf("[Douban] 写入榜单缓存失败: %v\n", err)
	}
}

func trimRanking(ranking model.DoubanRankingResponse, limit int) model.DoubanRankingResponse {
	if limit >= len(ranking.Items) {
		ranking.Total = len(ranking.Items)
		return ranking
	}
	ranking.Items = ranking.Items[:limit]
	ranking.Total = len(ranking.Items)
	return ranking
}

func parseLimit(raw string) int {
	if raw == "" {
		return defaultLimit
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return defaultLimit
	}
	return v
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

func toFloat64(value interface{}) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, err := v.Float64()
		if err == nil {
			return f
		}
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return f
		}
	}
	return 0
}

type doubanCollectionResp struct {
	Total                  int             `json:"total"`
	SubjectCollectionItems []doubanSubject `json:"subject_collection_items"`
}

type doubanSubject struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Rating struct {
		Value interface{} `json:"value"`
	} `json:"rating"`
}
