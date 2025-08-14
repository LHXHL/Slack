package webscan

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"slack-wails/core/subdomain"
	"slack-wails/core/waf"
	"slack-wails/lib/gologger"
	"slack-wails/lib/gomessage"
	"slack-wails/lib/structs"
	"slack-wails/lib/utils/arrayutil"
	"slack-wails/lib/utils/httputil"
	"slack-wails/lib/utils/randutil"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/qiwentaidi/clients"

	"github.com/go-resty/resty/v2"
	"github.com/panjf2000/ants/v2"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const maxInfoReponseSize = 1024 * 100 // 100KB

var IsRunning = false // 用于判断网站扫描是否正在运行

type WebInfo struct {
	Protocol      string
	Port          int
	Path          string
	Title         string
	StatusCode    int
	IconHash      string // mmh3
	IconMd5       string // md5
	BodyString    string
	HeadeString   string
	ContentType   string
	Server        string
	ContentLength int
	Banner        string // tcp指纹
	Cert          string // TLS证书
}

type FingerScanner struct {
	ctx                     context.Context
	taskId                  string // 任务ID
	urls                    []*url.URL
	aliveURLs               []*url.URL          // 默认指纹扫描结束后，存活的URL，以便后续主动指纹过滤目标
	screenshot              bool                // 是否截屏
	thread                  int                 // 指纹线程
	deepScan                bool                // 代表主动指纹探测
	rootPath                bool                // 主动指纹是否采取根路径扫描
	basicURLWithFingerprint map[string][]string // 后续nuclei需要扫描的目标列表
	headers                 map[string]string   // 请求头
	generateLog4j2          bool                // 是否添加Log4j2指纹，后续nuclei可以添加扫描
	client                  *resty.Client
	notFollowClient         *resty.Client
	mutex                   sync.RWMutex
}

func NewWebscanEngine(ctx context.Context, taskId string, proxyURL string, options structs.WebscanOptions) *FingerScanner {
	urls := make([]*url.URL, 0, len(options.Target)) // 提前分配容量
	waitChecks := []string{}
	client := clients.NewRestyClientWithProxy(nil, true, proxyURL)
	hasNoProtocol := false
	for _, t := range options.Target {
		t = strings.TrimRight(t, "/")
		if !strings.Contains(t, "://") {
			hasNoProtocol = true
			waitChecks = append(waitChecks, t)
		} else {
			u, err := url.Parse(t)
			if err != nil {
				gologger.Error(ctx, err)
				continue
			}
			urls = append(urls, u)
		}
	}

	if hasNoProtocol {
		gomessage.Info(ctx, "存在未包含协议的目标, 程序正在识别http/https, 详情见日志")
		gologger.DualLog(ctx, gologger.Level_INFO, "存在未包含协议的目标, 程序正在识别http/https")
		for _, t := range waitChecks {
			gologger.Info(ctx, t+": 正在识别http/https")
			if url, err := clients.CheckProtocol(t, client); err != nil {
				continue
			} else {
				t = url
			}
			u, err := url.Parse(t)
			if err != nil {
				gologger.Error(ctx, err)
				continue
			}
			urls = append(urls, u)
		}
	}

	// 可以兼容其他协议目标进行漏洞扫描
	basicURLWithFingerprint := make(map[string][]string)
	var mutex sync.RWMutex
	for target, fingerprints := range options.TcpTarget {
		if len(fingerprints) > 0 {
			mutex.Lock()
			basicURLWithFingerprint[target] = fingerprints
			mutex.Unlock()
		}
	}
	if len(urls) == 0 && len(basicURLWithFingerprint) == 0 {
		gologger.Error(ctx, "No available targets found, please check input")
		return nil
	}
	return &FingerScanner{
		ctx:                     ctx,
		taskId:                  taskId,
		urls:                    urls,
		client:                  client,
		notFollowClient:         clients.NewRestyClientWithProxy(nil, false, proxyURL),
		screenshot:              options.Screenshot,
		thread:                  options.Thread,
		deepScan:                options.DeepScan,
		rootPath:                options.RootPath,
		basicURLWithFingerprint: basicURLWithFingerprint,
		headers:                 clients.Str2HeadersMap(options.CustomHeaders),
		generateLog4j2:          options.GenerateLog4j2,
	}
}

func (s *FingerScanner) FingerScan(ctrlCtx context.Context) {
	var wg sync.WaitGroup
	single := make(chan struct{})
	retChan := make(chan structs.InfoResult, len(s.urls))
	go func() {
		for pr := range retChan {
			runtime.EventsEmit(s.ctx, "webFingerScan", pr)
		}
		close(single)
	}()
	// 指纹扫描
	fscan := func(u *url.URL) {
		if ctrlCtx.Err() != nil {
			return
		}
		var (
			rawHeaders  []byte
			server      string
			contentType string
			statusCode  int
		)

		// 先进行一次不会重定向的扫描，可以获得重定向前页面的响应头中获取指纹
		resp, err := clients.DoRequest("GET", u.String(), s.headers, nil, 10, s.notFollowClient)
		if err == nil && resp.StatusCode() == 302 {
			rawHeaders = httputil.DumpResponseHeadersOnly(resp.RawResponse)
		}

		// 过滤CDN
		if resp.StatusCode() == 422 {
			retChan <- structs.InfoResult{
				URL:        u.String(),
				StatusCode: 422,
				Scheme:     u.Scheme,
			}
			return
		}

		// 正常请求指纹
		resp, err = clients.DoRequest("GET", u.String(), s.headers, nil, 10, s.client)
		if err != nil {
			if len(rawHeaders) > 0 {
				gologger.Debug(s.ctx, fmt.Sprintf("%s has error to 302, response headers: %s", u.String(), string(rawHeaders)))
				statusCode = 302
			} else {
				retChan <- structs.InfoResult{
					URL:        u.String(),
					StatusCode: 0,
					Scheme:     u.Scheme,
				}
				return
			}
		}

		body := httputil.LimitResponseBytes(resp.Body(), maxInfoReponseSize)
		// 合并请求头数据
		rawHeaders = append(rawHeaders, httputil.DumpResponseHeadersOnly(resp.RawResponse)...)

		// 请求Logo
		faviconHash, faviconMd5 := FaviconHash(u, s.headers, s.client)

		// 发送shiro探测
		rawHeaders = append(rawHeaders, fmt.Appendf(nil, "Set-Cookie: %s", s.ShiroScan(u))...)

		// 跟随JS重定向，并替换成重定向后的数据
		redirectBody := s.GetJSRedirectResponse(u, string(body))
		if redirectBody != nil {
			// JS重定向后，body数据不应该直接覆盖 fix in 2.0.8
			body = append(body, redirectBody...)
			// body = redirectBody
		}
		// 网站正常响应
		title := clients.GetTitle(body)
		server = resp.Header().Get("Server")
		contentType = resp.Header().Get("Content-Type")
		statusCode = resp.StatusCode()
		web := &WebInfo{
			HeadeString:   strings.ToLower(string(rawHeaders)),
			ContentType:   strings.ToLower(contentType),
			Cert:          strings.ToLower(GetTLSString(u.Scheme, u.Host)),
			BodyString:    strings.ToLower(string(body)),
			Path:          strings.ToLower(u.Path),
			Title:         strings.ToLower(title),
			Server:        strings.ToLower(server),
			ContentLength: len(resp.Body()),
			Port:          httputil.GetPort(u),
			IconHash:      faviconHash,
			IconMd5:       faviconMd5,
			StatusCode:    statusCode,
		}

		wafInfo := *waf.ResolveAndWafIdentify(u.Hostname(), subdomain.DefaultDnsServers)

		s.aliveURLs = append(s.aliveURLs, u)

		fingerprints := Scan(s.ctx, web, FingerprintDB)

		if s.generateLog4j2 {
			fingerprints = append(fingerprints, "Generate-Log4j2")
		}

		if s.FastjsonScan(u) {
			fingerprints = append(fingerprints, "Fastjson")
		}

		if checkHoneypotWithHeaders(web.HeadeString) || checkHoneypotWithFingerprintLength(len(fingerprints)) {
			fingerprints = []string{"疑似蜜罐"}
		}

		// 截屏
		var screenshotPath string
		// 截屏条件要满足协议, fix in v2.0.8
		if s.screenshot && (u.Scheme == "https" || u.Scheme == "http") {
			if screenshotPath, err = GetScreenshot(u.String()); err != nil {
				gologger.Debug(s.ctx, err)
			}
		}

		s.mutex.Lock()
		s.basicURLWithFingerprint[u.String()] = append(s.basicURLWithFingerprint[u.String()], fingerprints...)
		s.mutex.Unlock()

		retChan <- structs.InfoResult{
			TaskId:       s.taskId,
			URL:          u.String(),
			Scheme:       u.Scheme,
			Host:         u.Host,
			Port:         web.Port,
			StatusCode:   web.StatusCode,
			Length:       web.ContentLength,
			Title:        title,
			Fingerprints: fingerprints,
			IsWAF:        wafInfo.Exsits,
			WAF:          wafInfo.Name,
			Detect:       "Default",
			Screenshot:   screenshotPath,
		}
	}
	threadPool, _ := ants.NewPoolWithFunc(s.thread, func(target interface{}) {
		defer wg.Done()
		if ctrlCtx.Err() != nil {
			return
		}
		t := target.(*url.URL)
		fscan(t)
	})
	defer threadPool.Release()
	for _, target := range s.urls {
		if ctrlCtx.Err() != nil {
			return
		}
		wg.Add(1)
		threadPool.Invoke(target)
	}
	wg.Wait()
	close(retChan)
	gologger.Info(s.ctx, "FingerScan Finished")
	<-single
}

type ActiveFingerDetect struct {
	URL  *url.URL
	Fpe  []FingerPEntity
	Path string
}

const activeTimeoutLimit = 15 // 超过该次数就不再扫描该目标
func (s *FingerScanner) ActiveFingerScan(ctrlCtx context.Context) {
	if len(s.aliveURLs) == 0 {
		gologger.Warning(s.ctx, "No surviving target found, active fingerprint scanning has been skipped")
		return
	}
	gologger.Info(s.ctx, "Active fingerprint detection in progress")
	var id int32
	var wg sync.WaitGroup
	visited := sync.Map{}        // 记录已访问路径
	timeoutCounter := sync.Map{} // 记录每个目标的超时次数

	single := make(chan struct{})
	retChan := make(chan structs.InfoResult, len(s.urls))

	go func() {
		for pr := range retChan {
			runtime.EventsEmit(s.ctx, "webFingerScan", pr)
		}
		close(single)
	}()

	// 主动指纹扫描线程池
	threadPool, _ := ants.NewPoolWithFunc(s.thread, func(tfp interface{}) {
		defer wg.Done()
		fp := tfp.(ActiveFingerDetect)
		fullURL := fp.URL.String() + fp.Path
		baseURL := fp.URL.String()

		// 检查是否已超出超时限制
		if val, ok := timeoutCounter.Load(baseURL); ok && val.(int) >= activeTimeoutLimit {
			gologger.DualLog(s.ctx, gologger.Level_WARN, fmt.Sprintf("Target %s has reached the timeout limit, skipping active scan", baseURL))
			return
		}

		// 去重：URL + path
		// 使用 sync.Map 检查是否已访问
		if _, ok := visited.Load(fullURL); ok {
			return
		}
		visited.Store(fullURL, true)

		resp, err := clients.DoRequest("GET", fullURL, s.headers, nil, 5, s.client)
		if err != nil {
			// 累计超时次数
			v, _ := timeoutCounter.LoadOrStore(baseURL, 1)
			timeoutCounter.Store(baseURL, v.(int)+1)
			return
		}

		body := resp.Body()
		server := resp.Header().Get("Server")
		contentType := resp.Header().Get("Content-Type")
		title := clients.GetTitle(body)

		headers, _, _ := httputil.DumpResponseHeadersAndRaw(resp.RawResponse)
		ti := &WebInfo{
			HeadeString:   strings.ToLower(string(headers)),
			ContentType:   strings.ToLower(contentType),
			BodyString:    strings.ToLower(string(body)),
			Path:          strings.ToLower(fp.Path),
			Title:         strings.ToLower(title),
			Server:        strings.ToLower(server),
			ContentLength: len(body),
			Port:          httputil.GetPort(fp.URL),
			StatusCode:    resp.StatusCode(),
		}
		result := Scan(s.ctx, ti, fp.Fpe)

		if (len(result) > 0 && ti.StatusCode != 404) || arrayutil.ArrayContains("ThinkPHP", result) {
			s.mutex.Lock()
			s.basicURLWithFingerprint[fp.URL.String()] = append(s.basicURLWithFingerprint[fp.URL.String()], result...)
			s.mutex.Unlock()

			retChan <- structs.InfoResult{
				TaskId:       s.taskId,
				URL:          fullURL,
				StatusCode:   ti.StatusCode,
				Length:       ti.ContentLength,
				Title:        title,
				Fingerprints: []string{fp.Fpe[0].ProductName},
				Detect:       "Active",
				Port:         ti.Port,
				Scheme:       fp.URL.Scheme,
				Host:         fp.URL.Host,
			}
		}
	})
	defer threadPool.Release()

	s.ActiveCounts()

	// 开始提交任务
	for _, target := range s.aliveURLs {
		for _, item := range ActiveFingerprintDB {
			for _, path := range item.Path {
				if ctrlCtx.Err() != nil {
					return
				}

				base := target.String()
				if val, ok := timeoutCounter.Load(base); ok && val.(int) >= activeTimeoutLimit {
					s.IncreaseActiveProgress(&id)
					continue // 已超时限制，跳过该目标
				}

				wg.Add(1)
				s.IncreaseActiveProgress(&id)

				if s.rootPath {
					target, _ = url.Parse(httputil.GetBasicURL(target.String()))
				}

				threadPool.Invoke(ActiveFingerDetect{
					URL:  target,
					Fpe:  item.Fpe,
					Path: path,
				})
			}
		}
	}

	wg.Wait()
	close(retChan)
	gologger.Info(s.ctx, "ActiveFingerScan Finished")
	<-single
}

// 统计主动指纹总共要扫描的目标
func (s *FingerScanner) ActiveCounts() {
	var id = 0
	for _, afdb := range ActiveFingerprintDB {
		id += len(afdb.Path)
	}
	count := len(s.aliveURLs) * id
	runtime.EventsEmit(s.ctx, "ActiveCounts", count)
}

func (s *FingerScanner) IncreaseActiveProgress(id *int32) {
	atomic.AddInt32(id, 1) // 补上进度递增
	runtime.EventsEmit(s.ctx, "ActiveProgressID", id)
}

func (s *FingerScanner) URLWithFingerprintMap() map[string][]string {
	return s.basicURLWithFingerprint
}

func Scan(ctx context.Context, web *WebInfo, targetDB []FingerPEntity) []string {
	var fingerPrintResults []string

	for _, finger := range targetDB {
		expr := finger.AllString
		for _, rule := range finger.Rule {
			var result bool
			switch rule.Key {
			case "header":
				result = dataCheckString(rule.Op, web.HeadeString, rule.Value)
			case "body":
				result = dataCheckString(rule.Op, web.BodyString, rule.Value)
			case "server":
				result = dataCheckString(rule.Op, web.Server, rule.Value)
			case "title":
				result = dataCheckString(rule.Op, web.Title, rule.Value)
			case "cert":
				result = dataCheckString(rule.Op, web.Cert, rule.Value)
			case "port":
				value, err := strconv.Atoi(rule.Value)
				if err == nil {
					result = dataCheckInt(rule.Op, web.Port, value)
				}
			case "protocol":
				result = (rule.Op == 0 && web.Protocol == rule.Value) || (rule.Op == 1 && web.Protocol != rule.Value)
			case "path":
				result = dataCheckString(rule.Op, web.Path, rule.Value)
			case "icon_hash":
				value, err := strconv.Atoi(rule.Value)
				hashIcon, errHash := strconv.Atoi(web.IconHash)
				if err == nil && errHash == nil {
					result = dataCheckInt(rule.Op, hashIcon, value)
				}
			case "icon_mdhash":
				result = dataCheckString(rule.Op, web.IconMd5, rule.Value)
			case "status":
				value, err := strconv.Atoi(rule.Value)
				if err == nil {
					result = dataCheckInt(rule.Op, web.StatusCode, value)
				}
			case "content_type":
				result = dataCheckString(rule.Op, web.ContentType, rule.Value)
			case "banner":
				result = dataCheckString(rule.Op, web.Banner, rule.Value)
			}

			if result {
				expr = expr[:rule.Start] + "T" + expr[rule.End:]
			} else {
				expr = expr[:rule.Start] + "F" + expr[rule.End:]
			}
		}

		r, err := boolEval(expr)
		if err != nil {
			gologger.DualLog(ctx, gologger.Level_ERROR, fmt.Sprintf("[fingerprint] 错误指纹: %v", finger.AllString))
			continue
		}
		if r {
			fingerPrintResults = append(fingerPrintResults, finger.ProductName)
		}
	}

	return arrayutil.RemoveDuplicates(fingerPrintResults)
}

func (s *FingerScanner) GetJSRedirectResponse(u *url.URL, respRaw string) []byte {
	var nextCheckUrl string
	newPath := checkJSRedirect(respRaw)
	// 跳转到ie.html需要忽略，fix in v1.7.5
	if newPath == "" || newPath == "/html/ie.html" {
		return nil
	}
	newPath = strings.Trim(newPath, " ")
	newPath = strings.Trim(newPath, "'")
	newPath = strings.Trim(newPath, "\"")
	if strings.HasPrefix(newPath, "https://") || strings.HasPrefix(newPath, "http://") {
		if strings.Contains(newPath, u.Host) {
			nextCheckUrl = newPath
		}
	} else {
		if len(newPath) > 0 {
			if newPath[0] == '/' {
				newPath = newPath[1:]
			}
		}
		nextCheckUrl = getRealPath(u.Scheme+"://"+u.Host) + "/" + newPath

	}
	resp, err := clients.SimpleGet(nextCheckUrl, s.client)
	if err != nil {
		return nil
	}
	return resp.Body()
}

// 探测shiro并返回响应头中的Set-Cookie值
func (s *FingerScanner) ShiroScan(u *url.URL) string {
	shiroHeader := map[string]string{
		"Cookie": fmt.Sprintf("JSESSIONID=%s;rememberMe=123", randutil.RandomStr(16)),
	}
	resp, err := clients.DoRequest("GET", u.String(), shiroHeader, nil, 10, s.client)
	if err != nil {
		return ""
	}
	return resp.Header().Get("Set-Cookie")
}

// 探测Fastjson
// {"\u+040\u+074\u+079\u+070\u+065":"java.lang.AutoCloseabl\u+065" unicode 绕过 waf
func (s *FingerScanner) FastjsonScan(u *url.URL) bool {
	jsonHeader := map[string]string{
		"Content-Type": "application/json",
	}
	resp, err := clients.DoRequest("POST", u.String(), jsonHeader, strings.NewReader(`{"\u+040\u+074\u+079\u+070\u+065":"java.lang.AutoCloseabl\u+065"`), 10, s.client)
	if err != nil {
		return false
	}
	return bytes.Contains(resp.Body(), []byte("fastjson-version"))
}
