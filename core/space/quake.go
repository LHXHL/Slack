package space

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slack-wails/lib/structs"
	"slack-wails/lib/utils/arrayutil"
	"strconv"
	"strings"

	"github.com/qiwentaidi/clients"
)

const (
	quakeTipsApi   = "https://quake.360.net/api/visitor/search/app"
	quakeServerApi = "https://quake.360.net/api/v3/search/quake_service"
	quakeUserApi   = "https://quake.360.net/api/v3/user/info"
)

func QuakeApiSearch(o *structs.QuakeRequestOptions) *structs.QuakeResult {
	var startIndex int
	if o.PageNum == 1 {
		startIndex = 0
	} else {
		startIndex = (o.PageNum - 1) * o.PageSize
	}
	data := make(map[string]interface{})
	if len(o.IpList) > 0 {
		data["ip_list"] = o.IpList
	} else {
		data["query"] = o.Query
	}
	data["start"] = startIndex
	data["size"] = o.PageSize
	data["latest"] = o.Latest
	data["shortcuts"] = getShortcuts(o)
	bytesData, _ := json.Marshal(data)
	header := map[string]string{
		"Content-Type": "application/json",
		"X-QuakeToken": o.Token,
	}
	resp, err := clients.DoRequest("POST", quakeServerApi, header, bytes.NewReader(bytesData), 20, clients.NewRestyClient(nil, true))
	if err != nil {
		return &structs.QuakeResult{
			Code:    502,
			Message: err.Error(),
		}
	}
	bodyStr := string(resp.Body())
	if bodyStr == "/quake/login" {
		return &structs.QuakeResult{
			Code:    302,
			Message: "Token is error",
		}
	}
	if bodyStr == "暂不支持搜索该内容" {
		return &structs.QuakeResult{
			Code:    302,
			Message: bodyStr,
		}
	}
	var qrk structs.QuakeRawResult
	json.Unmarshal(resp.Body(), &qrk)
	qk := &structs.QuakeResult{
		Message: qrk.Message,
		Total:   qrk.Meta.Pagination.Total,
		Credit:  QuakeUserSearch(o.Token),
	}
	if code, err := strconv.Atoi(fmt.Sprintf("%v", qrk.Code)); err == nil {
		qk.Code = code
	} else {
		qk.Code = 500
	}
	for _, item := range qrk.Data {
		var components []string
		var protocol string
		for _, v := range item.Components {
			if v.ProductNameEN == "" {
				components = append(components, arrayutil.MergeNonEmpty([]string{v.ProductNameCN, v.Version}, "/"))
			} else {
				components = append(components, arrayutil.MergeNonEmpty([]string{v.ProductNameEN, v.Version}, "/"))
			}
		}
		if item.Service.Name == "http/ssl" {
			protocol = "https"
		} else {
			protocol = item.Service.Name
		}
		qk.Data = append(qk.Data, structs.QuakeData{
			URL:        mergeURL(protocol, item.Service.HTTP.Host, item.IP, item.Port),
			Components: components,
			Port:       item.Port,
			Protocol:   protocol,
			Host:       item.Service.HTTP.Host,
			Title:      item.Service.HTTP.Title,
			IcpName:    item.Service.HTTP.ICP.Main_licence.Unit,
			IcpNumber:  item.Service.HTTP.ICP.Main_licence.Licence,
			CertName:   extractOrg(item.Service.Cert),
			IP:         item.IP,
			Isp:        item.Location.ISP,
			FaviconURL: item.Service.HTTP.Favicon.S3URL,
			Position: arrayutil.MergePosition(structs.Position{
				Province:  item.Location.ProvinceCN,
				City:      item.Location.CityCN,
				District:  item.Location.DistrictCN,
				Connector: "/",
			}),
		})
	}
	qrk = structs.QuakeRawResult{} // 清空内存
	return qk
}

// 查询剩余积分
func QuakeUserSearch(token string) int {
	header := map[string]string{
		"Content-Type": "application/json",
		"X-QuakeToken": token,
	}
	resp, err := clients.DoRequest("GET", quakeUserApi, header, nil, 10, clients.NewRestyClient(nil, true))
	if err != nil {
		return 0
	}
	var qui structs.QuakeUserInfo
	if err := json.Unmarshal(resp.Body(), &qui); err != nil {
		return 0
	}
	return qui.Data.Credit
}

func SearchQuakeTips(query string) *structs.QuakeTipsResult {
	var qs structs.QuakeTipsResult
	jsonData := fmt.Sprintf(`{"app_name":"%v","device":{"UUID":"aa963dba-1bfa-54cf-9fdd-7b9be5a30890"}}`, query)
	header := map[string]string{
		"Content-Type": "application/json",
	}
	resp, err := clients.DoRequest("POST", quakeTipsApi, header, strings.NewReader(jsonData), 10, clients.NewRestyClient(nil, true))
	if err != nil {
		return &qs
	}
	json.Unmarshal(resp.Body(), &qs)
	return &qs
}

// 首次请求不能是带Shortcuts，需要在请求一次quake之后，获取到正确的cert_common值
func getShortcuts(o *structs.QuakeRequestOptions) []string {
	if o.CertCommon == "" {
		return []string{}
	}
	var (
		shortcutsMeta         structs.ShortcutsMeta
		cdn, honepot, invalid string
		shortcuts             []string
	)
	header := map[string]string{
		"Cookie": "cert_common=" + o.CertCommon,
	}
	resp, err := clients.DoRequest("GET", "https://quake.360.net/api/search/shortcuts/quake_service_unique", header, nil, 10, clients.NewRestyClient(nil, true))
	if err != nil {
		return shortcuts
	}
	// 未登录状态直接返回
	if string(resp.Body()) == "/quake/login" {
		return shortcuts
	}
	json.Unmarshal(resp.Body(), &shortcutsMeta)
	for _, v := range shortcutsMeta.Data {
		if v.Name == "排除CDN" {
			cdn = v.Id
		}
		if v.Name == "排除蜜罐" {
			honepot = v.Id
		}
		if v.Name == "过滤无效请求" {
			invalid = v.Id
		}
	}

	if o.CDN {
		shortcuts = append(shortcuts, cdn)
	}
	if o.Honeypot {
		shortcuts = append(shortcuts, honepot)
	}
	if o.Invalid {
		shortcuts = append(shortcuts, invalid)
	}
	return shortcuts
}

func mergeURL(protocol, domain, ip string, port int) string {
	host := domain
	if host == "" {
		host = ip
	}
	if port == 80 || port == 443 {
		return fmt.Sprintf("%s://%s", protocol, host)
	}
	return fmt.Sprintf("%s://%s:%d", protocol, host, port)
}

func extractOrg(cert string) string {
	re := regexp.MustCompile(`Subject:.*?O=([^,]+)`)
	match := re.FindStringSubmatch(cert)
	if len(match) > 1 {
		return match[1]
	}
	return ""
}
