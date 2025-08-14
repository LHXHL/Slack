package space

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"slack-wails/lib/gologger"
	"slack-wails/lib/structs"
	"slack-wails/lib/utils/arrayutil"
	"strconv"
	"time"

	"github.com/qiwentaidi/clients"
)

const TipApi = "https://api.fofa.info/v1/search/tip?"

type FofaClient struct {
	AppId      string // tips
	PrivateKey string // tips
	Auth       *structs.FofaAuth
}

func NewFofaConfig(auth *structs.FofaAuth) *FofaClient {
	const appid = "9e9fb94330d97833acfbc041ee1a76793f1bc691"
	const privatekey = `MIIEvAIBADANBgkqhkiG9w0BAQEFAASCBKYwggSiAgEAAoIBAQC/TGN5+4FMXo7H3jRmostQUUEO1NwH10B8ONaDJnYDnkr5V0ZzUvkuola7JGSFgYVOUjgrmFGITG+Ne7AgR53Weiunlwp15MsnCa8/IWBoSHs7DX1O72xNHmEfFOGNPyJ4CsHaQ0B2nxeijs7wqKGYGa1snW6ZG/ZfEb6abYHI9kWVN1ZEVTfygI+QYqWuX9HM4kpFgy/XSzUxYE9jqhiRGI5f8SwBRVp7rMpGo1HZDgfMlXyA5gw++qRq7yHA3yLqvTPSOQMYJElJb12NaTcHKLdHahJ1nQihL73UwW0q9Zh2c0fZRuGWe7U/7Bt64gV2na7tlA62A9fSa1Dbrd7lAgMBAAECggEAPrsbB95MyTFc2vfn8RxDVcQ/dFCjEsMod1PgLEPJgWhAJ8HR7XFxGzTLAjVt7UXK5CMcHlelrO97yUadPAigHrwTYrKqEH0FjXikiiw0xB24o2XKCL+EoUlsCdg8GqhwcjL83Mke84c6Jel0vQBfdVQ+RZbetMCxqv1TpqpwW+iswlDY0+OKNxcDSnUyVkBko4M7bCqJ19DjzuHHLRmSuJhWLjX2PzdrVwIrRChxeJRR5AzrNE2BC/ssKasWjZfgkTOW6MS96q+wMLgwFGCQraU0f4AW5HA4Svg8iWT2uukcDg7VXXc/eEmkfmDGzmgsszUJZYb1hYsvjgbMP1ObwQKBgQDw1K0xfICYctiZ3aHS7mOk0Zt6B/3rP2z9GcJVs0eYiqH+lteLNy+Yx4tHtrQEuz16IKmM1/2Ghv8kIlOazpKaonk3JEwm1mCEXpgm4JI7UxPGQj/pFTCavKBBOIXxHJVSUSg0nKFkJVaoJiNy0CKwQNoFGdROk2fSYu8ReB/WlQKBgQDLWQR3RioaH/Phz8PT1ytAytH+W9M4P4tEx/2Uf5KRJxPQbN00hPnK6xxHAqycTpKkLkbJIkVWEKcIGxCqr6iGyte3xr30bt49MxIAYrdC0LtBLeWIOa88GTqYmIusqJEBmiy+A+DudM/xW4XRkgrOR1ZsagzI3FUVlei9DwFjEQKBgG8JH3EZfhDLoqIOVXXzA24SViTFWoUEETQAlGD+75udD2NaGLbPEtrV5ZmC2yzzRzzvojyVuQY1Z505VmKhq2YwUsLhsVqWrJlbI7uI/uLrQsq98Ml+Q5KUNS7c6KRqEU6KrIbVUHPj4zhTnTRqUhQBUoPXjNNNkyilBKSBReyhAoGAd3xGCIPdB17RIlW/3sFnM/o5bDmuojWMcw0ErvZLPCl3Fhhx3oNod9iw0/T5UhtFRV2/0D3n+gts6nFk2LbA0vtryBvq0C85PUK+CCX5QzR9Y25Bmksy8aBtcu7n27ttAUEDm1+SEuvmqA68Ugl7efwnBytFed0lzbo5eKXRjdECgYAk6pg3YIPi86zoId2dC/KfsgJzjWKVr8fj1+OyInvRFQPVoPydi6iw6ePBsbr55Z6TItnVFUTDd5EX5ow4QU1orrEqNcYyG5aPcD3FXD0Vq6/xrYoFTjZWZx23gdHJoE8JBCwigSt0KFmPyDsN3FaF66Iqg3iBt8rhbUA8Jy6FQA==`
	return &FofaClient{
		AppId:      appid,
		PrivateKey: privatekey,
		Auth:       auth,
	}
}

func (f *FofaClient) GetTips(key string) ([]byte, error) {
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	signParam := "q" + key + "ts" + ts
	params := url.Values{}
	params.Set("q", key)
	params.Set("ts", ts)
	params.Set("sign", f.GetInputSign(signParam))
	params.Set("app_id", f.AppId)
	resp, err := clients.SimpleGet(TipApi+params.Encode(), clients.DefaultRestyClient())
	if err != nil {
		return nil, err
	}
	return resp.Body(), nil
}

func (f *FofaClient) GetInputSign(inputString string) string {
	data := []byte(inputString)
	keyBytes, err := base64.StdEncoding.DecodeString(f.PrivateKey)
	if err != nil {
		return ""
	}
	privateKey, err := x509.ParsePKCS8PrivateKey(keyBytes)
	if err != nil {
		return ""
	}
	hash := sha256.New()
	hash.Write(data)
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey.(*rsa.PrivateKey), crypto.SHA256, hash.Sum(nil))
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(signature)
}

// fofa base64加密接口
func FOFABaseEncode(str string) string {
	return base64.StdEncoding.EncodeToString([]byte(str))
}

func (f *FofaClient) FofaApiSearch(ctx context.Context, search, pageSize, pageNum string, fraud, cert bool) *structs.FofaSearchResult {
	var fs structs.FofaSearchResult // 期望返回结构体
	var fr structs.FofaResult       // 原始数据
	address := f.Auth.Address + "api/v1/search/all?email=" + f.Auth.Email + "&key=" + f.Auth.Key + "&qbase64=" +
		FOFABaseEncode(search) + "&cert.is_valid" + fmt.Sprint(cert) + fmt.Sprintf("&is_fraud=%v&is_honeypot=%v", fmt.Sprint(fraud), fmt.Sprint(fraud)) +
		"&page=" + pageNum + "&size=" + pageSize + "&fields=host,title,ip,domain,port,protocol,country_name,region,city,icp,link,product"
	resp, err := clients.SimpleGet(address, clients.NewRestyClient(nil, true))
	if err != nil {
		gologger.Debug(ctx, err)
		fs.Error = true
		fs.Message = "请求失败"
		return &fs
	}
	json.Unmarshal(resp.Body(), &fr)
	fs.Size = fr.Size
	fs.Error = fr.Error
	if fr.Errmsg == "[820001] 没有权限搜索product字段" {
		user := f.FofaApiSearchByUserInfo(ctx)
		if user.Error || !user.Isvip {
			fs.Message = "[FOFA] 当前用户非VIP权限, 无法使用API查询"
		}
	} else {
		fs.Message = fr.Errmsg
	}
	if !fs.Error {
		if fs.Size == 0 {
			fs.Message = "未查询到数据"
		} else {
			for i := range fr.Results {
				fs.Results = append(fs.Results, structs.Results{
					URL:      fr.Results[i][10],
					Host:     fr.Results[i][0],
					Title:    fr.Results[i][1],
					IP:       fr.Results[i][2],
					Port:     fr.Results[i][4],
					Domain:   fr.Results[i][3],
					Protocol: fr.Results[i][5],
					Region: arrayutil.MergePosition(structs.Position{
						Country:   fr.Results[i][6],
						Province:  fr.Results[i][7],
						City:      fr.Results[i][8],
						Connector: "/",
					}),
					ICP:     fr.Results[i][9],
					Product: fr.Results[i][11],
				})
			}
		}
	}
	time.Sleep(time.Second)
	return &fs
}

func (f *FofaClient) FofaApiSearchByUserInfo(ctx context.Context) *structs.FofaUserInfo {
	var user structs.FofaUserInfo
	resp, err := clients.SimpleGet("https://fofa.info/api/v1/info/my?key="+f.Auth.Key, clients.NewRestyClient(nil, true))
	if err != nil {
		user.Error = true
		return &user
	}
	json.Unmarshal(resp.Body(), &user)
	return &user
}
