package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
	"golang.org/x/sync/errgroup"
)

const BingSearchEndpoint = "https://api.bing.microsoft.com/v7.0/search"

var WebClick = openai.Tool{
	Type: openai.ToolTypeFunction,
	Function: &openai.FunctionDefinition{
		Name:        "WebClick",
		Description: `用于同时打开多个网页的工具方法，主要用在需要快速从多个来源收集信息的情况下。这个方法允许一次性点击多个搜索结果链接，从而打开多个页面。`,
		Parameters: &jsonschema.Definition{
			Type:     jsonschema.Object,
			Required: []string{"urls"},
			Properties: map[string]jsonschema.Definition{
				"urls": {
					Type:        jsonschema.Array,
					Description: "一个包含你想要更详细查看的网页的url列表",
					Items: &jsonschema.Definition{
						Type:        jsonschema.String,
						Description: "网页的 url 地址",
					},
				},
			},
		},
	},
}

type WebClickInput struct {
	URLs []string `json:"urls"`
}

func Click(ctx context.Context, viewer Viewer, proxy string, urls ...string) ([]ClickResult, error) {
	e := new(errgroup.Group)
	m := &sync.Map{}
	for _, url := range urls {
		u := url
		e.Go(func() error {
			page, err := viewPage(ctx, viewer, proxy, u)
			if err != nil {
				return err
			}
			m.Store(u, page)
			return nil
		})
	}
	e.Wait()
	var res []ClickResult
	for _, url := range urls {
		u := url
		value, ok := m.Load(u)
		if !ok {
			continue
		}
		res = append(res, ClickResult{
			Url:     u,
			Content: value.(string),
		})
	}
	return res, nil
}

type ClickResult struct {
	Url     string `json:"url"`
	Content string `json:"content"`
}

var OpenUrl = openai.Tool{
	Type: openai.ToolTypeFunction,
	Function: &openai.FunctionDefinition{
		Name:        "OpenUrl",
		Description: `直接打开网页, 返回内容为指定网页的内容`,
		Parameters: &jsonschema.Definition{
			Type:     jsonschema.Object,
			Required: []string{"url"},
			Properties: map[string]jsonschema.Definition{
				"url": {
					Type:        jsonschema.String,
					Description: "需要直接访问的网页的完整URL地址。",
				},
			},
		},
	},
}

type OpenURLInput struct {
	Url string `json:"url"`
}

func OpenURL(ctx context.Context, viewer Viewer, proxy, u string) (string, error) {
	return viewPage(ctx, viewer, proxy, u)
}

var WebSearch = openai.Tool{
	Type: openai.ToolTypeFunction,
	Function: &openai.FunctionDefinition{
		Name: "WebSearch",
		Description: `执行一个搜索查询，返回与该查询相关的网页列表：
1. 网页的标题：给出了关于网页内容的初步信息。
2. URL：网页的地址链接，可以直接访问。
`,
		Parameters: &jsonschema.Definition{
			Type:     jsonschema.Object,
			Required: []string{"query"},
			Properties: map[string]jsonschema.Definition{
				"query": {
					Type:        jsonschema.String,
					Description: "你想要搜索的关键词或问题",
				},
			},
		},
	},
}

type WebSearchInput struct {
	Query string `json:"query"`
}

type SearchResult struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"-"`
}

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func Search(ctx context.Context, client HTTPClient, token string, searchTerm string) ([]SearchResult, error) {
	req, _ := http.NewRequest("GET", BingSearchEndpoint, nil)
	req = req.WithContext(ctx)
	param := req.URL.Query()
	param.Add("q", searchTerm)
	param.Add("count", "8")
	param.Add("safeSearch", "Strict")
	param.Add("setLang", "zh-hans")
	param.Add("mkt", "zh-CN")
	req.URL.RawQuery = param.Encode()

	req.Header.Add("Ocp-Apim-Subscription-Key", token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	ans := new(BingAnswer)
	if err = json.NewDecoder(resp.Body).Decode(&ans); err != nil {
		return nil, err
	}

	var results = make([]SearchResult, 0, len(ans.WebPages.Value))
	for _, result := range ans.WebPages.Value {
		results = append(results, SearchResult{
			Name:        result.Name,
			URL:         result.URL,
			Description: result.Snippet,
		})
	}

	return results, nil
}

// BingAnswer This struct formats the answers provided by the Bing Web Search API.
type BingAnswer struct {
	Type         string `json:"_type"`
	QueryContext struct {
		OriginalQuery string `json:"originalQuery"`
	} `json:"queryContext"`
	WebPages struct {
		WebSearchURL          string `json:"webSearchUrl"`
		TotalEstimatedMatches int    `json:"totalEstimatedMatches"`
		Value                 []struct {
			ID               string    `json:"id"`
			Name             string    `json:"name"`
			URL              string    `json:"url"`
			IsFamilyFriendly bool      `json:"isFamilyFriendly"`
			DisplayURL       string    `json:"displayUrl"`
			Snippet          string    `json:"snippet"`
			DateLastCrawled  time.Time `json:"dateLastCrawled"`
			SearchTags       []struct {
				Name    string `json:"name"`
				Content string `json:"content"`
			} `json:"searchTags,omitempty"`
			About []struct {
				Name string `json:"name"`
			} `json:"about,omitempty"`
		} `json:"value"`
	} `json:"webPages"`
	RelatedSearches struct {
		ID    string `json:"id"`
		Value []struct {
			Text         string `json:"text"`
			DisplayText  string `json:"displayText"`
			WebSearchURL string `json:"webSearchUrl"`
		} `json:"value"`
	} `json:"relatedSearches"`
	RankingResponse struct {
		Mainline struct {
			Items []struct {
				AnswerType  string `json:"answerType"`
				ResultIndex int    `json:"resultIndex"`
				Value       struct {
					ID string `json:"id"`
				} `json:"value"`
			} `json:"items"`
		} `json:"mainline"`
		Sidebar struct {
			Items []struct {
				AnswerType string `json:"answerType"`
				Value      struct {
					ID string `json:"id"`
				} `json:"value"`
			} `json:"items"`
		} `json:"sidebar"`
	} `json:"rankingResponse"`
}

type Viewer interface {
	View(ctx context.Context, proxy, urlPath string) (string, error)
}

type realViewer struct {
}

func NewRealViewer() Viewer {
	return &realViewer{}
}

func (r *realViewer) View(ctx context.Context, proxy, urlPath string) (string, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
	)
	if proxy != "" {
		opts = append(opts, chromedp.ProxyServer(proxy))
	}

	// 创建一个浏览器上下文环境
	ctx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()

	// 创建浏览器上下文
	ctx, cancel = chromedp.NewContext(ctx, chromedp.WithLogf(func(s string, i ...any) {
	}))
	defer cancel()

	// 设置超时
	ctx, cancel = context.WithTimeout(ctx, 150*time.Second)
	defer cancel()

	// 存储 <body> 内容的变量
	var bodyContent string

	// 设置要发送的自定义请求头
	headers := map[string]any{
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
	}

	// 运行任务
	if err := chromedp.Run(ctx,
		chromedp.Navigate(urlPath),
		network.Enable(),
		network.SetExtraHTTPHeaders(network.Headers(headers)),
		chromedp.Sleep(3*time.Second),
		chromedp.Text("html", &bodyContent),
	); err != nil {
		return "", err
	}

	return bodyContent, nil
}

func viewPage(ctx context.Context, viewer Viewer, proxy, urlPath string) (string, error) {
	type result struct {
		body string
		err  error
		from string
	}
	ch := make(chan *result, 2)
	ctx, cancelFn := context.WithCancel(ctx)
	defer cancelFn()
	go func() {
		s, err := viewer.View(ctx, "", urlPath)
		ch <- &result{body: s, err: err, from: "direct"}
	}()
	if proxy != "" {
		go func() {
			s, err := viewer.View(ctx, proxy, urlPath)
			ch <- &result{body: s, err: err, from: "proxy"}
		}()
	}
	res := <-ch
	return replaceBody(res.body), res.err
}

func replaceBody(s string) string {
	newlineRe := regexp.MustCompile(`\n+`)
	s = newlineRe.ReplaceAllString(s, "\n")
	spaceRe := regexp.MustCompile(`[ \t]+`)
	s = spaceRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	return s
}
