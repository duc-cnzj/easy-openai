package myai

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync"

	"github.com/samber/lo"
	"github.com/sashabaranov/go-openai"
	"golang.org/x/sync/errgroup"
)

type option struct {
	tools     []C
	sessionID string
}

func newOption() *option {
	return &option{}
}

type OptionFunc func(*option)

func WithTools(tools []C) func(*option) {
	return func(option *option) {
		option.tools = tools
	}
}
func WithSessionID(sessionID string) func(*option) {
	return func(option *option) {
		option.sessionID = sessionID
	}
}

const DefaultTipRole = "tip"

type FuncCallClient struct {
	client  *openai.Client
	TipRole string
}

func NewFuncCallClient(client *openai.Client) *FuncCallClient {
	cli := &FuncCallClient{client: client, TipRole: DefaultTipRole}

	return cli
}

type CallFn func(ctx context.Context, args *Args, c chan *openai.ChatCompletionStreamResponse) (string, error)

type C struct {
	Tool       openai.Tool
	CallFn     CallFn
	Concurrent bool
}

type Receiver struct {
	ch chan *openai.ChatCompletionStreamResponse
}

func (r *Receiver) Recv() (*openai.ChatCompletionStreamResponse, error) {
	resp, ok := <-r.ch
	if !ok {
		return nil, io.EOF
	}
	return resp, nil
}

func (c *FuncCallClient) CreateChatCompletionStream(
	ctx context.Context,
	req openai.ChatCompletionRequest,
	opts ...OptionFunc,
) (*Receiver, error) {
	opt := newOption()
	if len(req.Tools) > 0 {
		log.Println("[Warning]: tools already set")
	}
	for _, fn := range opts {
		fn(opt)
	}
	tools := make([]openai.Tool, 0, len(opt.tools))
	for _, call := range opt.tools {
		tools = append(req.Tools, call.Tool)
	}
	req.Tools = tools

	var mc map[string]C = make(map[string]C)
	for idx, call := range opt.tools {
		mc[call.Tool.Function.Name] = opt.tools[idx]
	}

	call, err := c.chatCompletionStreamWithCall(ctx, NewToolCallReq(req), mc)
	if err != nil {
		return nil, err
	}
	return &Receiver{ch: call}, nil
}

type ToolCallReq struct {
	sync.RWMutex
	req *openai.ChatCompletionRequest
}

func NewToolCallReq(req openai.ChatCompletionRequest) *ToolCallReq {
	return &ToolCallReq{req: &req}
}

func (t *ToolCallReq) AddMessage(message openai.ChatCompletionMessage) {
	t.Lock()
	defer t.Unlock()
	t.req.Messages = append(t.req.Messages, message)
}

func (t *ToolCallReq) ToOpenAIRequest() openai.ChatCompletionRequest {
	t.Lock()
	defer t.Unlock()
	return *t.req
}

func (c *FuncCallClient) chatCompletionStreamWithCall(ctx context.Context, req *ToolCallReq, calls map[string]C) (chan *openai.ChatCompletionStreamResponse, error) {
	completion, err := c.chatCompletion(ctx, req.ToOpenAIRequest())
	if err != nil {
		return nil, err
	}
	resCh := make(chan *openai.ChatCompletionStreamResponse, 100)
	go func() {
		defer close(resCh)
		var (
			isToolCall bool
			toolCalls  []openai.ToolCall
		)
		for resp := range completion {
			if len(resp.Choices) > 0 && len(resp.Choices[0].Delta.ToolCalls) > 0 {
				isToolCall = true
				toolCalls = resp.Choices[0].Delta.ToolCalls
				continue
			}
			resCh <- resp
		}

		if isToolCall {
			err = c.dealToolCalls(ctx, req, resCh, toolCalls, calls)
			if err != nil {
				resCh <- c.errorResp(err)
				return
			}
			streamCompletion, err := c.chatCompletionStreamWithCall(ctx, req, calls)
			if err != nil {
				resCh <- c.errorResp(err)
				return
			}
			for s := range streamCompletion {
				resCh <- s
			}
		}
	}()
	return resCh, nil
}

func (c *FuncCallClient) errorResp(err error) *openai.ChatCompletionStreamResponse {
	return &openai.ChatCompletionStreamResponse{
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Delta: openai.ChatCompletionStreamChoiceDelta{
					Content: err.Error(),
					Role:    openai.ChatMessageRoleAssistant,
				},
			},
		},
	}
}

func (c *FuncCallClient) chatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (chan *openai.ChatCompletionStreamResponse, error) {
	ch := make(chan *openai.ChatCompletionStreamResponse, 100)
	stream, err := c.client.CreateChatCompletionStream(
		ctx,
		req,
	)
	//indent, _ := json.MarshalIndent(req, "", "  ")
	//fmt.Println(string(indent))
	if err != nil {
		close(ch)
		return nil, err
	}
	go func() {
		defer func() {
			stream.Close()
			close(ch)
		}()

		var (
			toolCalls  []*openai.ToolCall
			isToolCall bool
		)

		for {
			var (
				streamErr error
				response  openai.ChatCompletionStreamResponse
			)
			response, streamErr = stream.Recv()
			if streamErr != nil {
				if !isToolCall && !errors.Is(streamErr, io.EOF) {
					ch <- &response
				}
				break
			}

			var hasContent bool
			for _, choice := range response.Choices {
				// 判断是不是 toolCall
				if len(choice.Delta.ToolCalls) > 0 {
					isToolCall = true
					for _, call := range choice.Delta.ToolCalls {
						toolCalls = fillToolCalls(toolCalls, call)
					}
					continue
				}

				if choice.Delta.Content != "" {
					hasContent = true
				}
			}

			if !hasContent || isToolCall {
				continue
			}

			if !isToolCall {
				ch <- &response
			}
		}
		if isToolCall {
			ch <- &openai.ChatCompletionStreamResponse{
				Choices: []openai.ChatCompletionStreamChoice{
					{
						Delta: openai.ChatCompletionStreamChoiceDelta{
							ToolCalls: lo.Map(toolCalls, func(item *openai.ToolCall, index int) openai.ToolCall {
								return *item
							}),
						},
					},
				},
			}
		}
	}()
	return ch, nil
}

func (c *FuncCallClient) dealToolCalls(ctx context.Context, tm *ToolCallReq, ch chan *openai.ChatCompletionStreamResponse, toolCalls []openai.ToolCall, calls2 map[string]C) error {
	tm.AddMessage(openai.ChatCompletionMessage{
		Role:      openai.ChatMessageRoleAssistant,
		ToolCalls: toolCalls,
	})

	callFn := func(ctx context.Context, call openai.ToolCall) error {
		var (
			err error
			res string
		)

		res, err = calls2[call.Function.Name].CallFn(ctx, NewArgs(call.Function.Arguments), ch)
		if err != nil {
			res = err.Error()
		}
		tm.AddMessage(openai.ChatCompletionMessage{
			Role:       openai.ChatMessageRoleTool,
			Content:    res,
			ToolCallID: call.ID,
		})
		return nil
	}

	if c.CanConcurrentDealToolCalls(toolCalls, calls2) {
		wg, ctx := errgroup.WithContext(ctx)
		for idx := range toolCalls {
			call := toolCalls[idx]
			wg.Go(func() error {
				return callFn(ctx, call)
			})
		}
		if err := wg.Wait(); err != nil {
			return err
		}
	} else {
		for idx := range toolCalls {
			call := toolCalls[idx]
			if err := callFn(ctx, call); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *FuncCallClient) CanConcurrentDealToolCalls(calls []openai.ToolCall, calls2 map[string]C) bool {
	for _, call := range calls {
		if callDef, ok := calls2[call.Function.Name]; ok && !callDef.Concurrent {
			return false
		}
	}
	return true
}

func fillToolCalls(calls []*openai.ToolCall, call openai.ToolCall) []*openai.ToolCall {
	last, _ := lo.Last(calls)
	if call.ID == "" && last != nil {
		last.Function.Arguments += call.Function.Arguments
	} else {
		calls = append(calls, &openai.ToolCall{
			Index: call.Index,
			ID:    lo.RandomString(30, lo.LettersCharset),
			Type:  call.Type,
			Function: openai.FunctionCall{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		})
	}
	return calls
}

func (c *FuncCallClient) NewTipResponse(s string) *openai.ChatCompletionStreamResponse {
	return &openai.ChatCompletionStreamResponse{
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Delta: openai.ChatCompletionStreamChoiceDelta{
					Content: strings.TrimRight(s, "\n") + "\n",
					Role:    c.TipRole,
				},
			},
		},
	}
}
