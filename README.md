# 轻松接入 function call

> github.com/sashabaranov/go-openai
>

```go
package main

import (
	"context"
	easyai "github.com/duc-cnzj/easy-openai"
	"github.com/sashabaranov/go-openai"
)

func main() {
	var openaiClient *openai.Client
	cli := easyai.NewFuncCallClient(openaiClient)
	// 兼容 openaiClient CreateChatCompletionStream
	cli.CreateChatCompletionStream(context.TODO(), openai.ChatCompletionRequest{})
}
```