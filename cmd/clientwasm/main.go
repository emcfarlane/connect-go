package main

import (
	"context"
	"fmt"
	"net/http"
	"syscall/js"

	"connectrpc.com/connect"
	pingv1 "connectrpc.com/connect/internal/gen/connect/ping/v1"
	"connectrpc.com/connect/internal/gen/connect/ping/v1/pingv1connect"
)

func ping(ctx context.Context, input string) (string, error) {
	client := pingv1connect.NewPingServiceClient(
		http.DefaultClient,
		"",
		connect.WithAcceptCompression("gzip", nil, nil),
	)
	rsp, err := client.Ping(ctx, connect.NewRequest(&pingv1.PingRequest{
		Text: input,
	}))
	if err != nil {
		return "", err

	}
	return rsp.Msg.Text, nil
}

func pingJS() js.Func {
	jsonFunc := js.FuncOf(func(this js.Value, args []js.Value) any {
		fmt.Println("pingJS")
		if len(args) != 1 {
			return "Invalid no of arguments passed"
		}
		input := args[0].String()
		fmt.Printf("input %s\n", input)
		ctx := context.Background()

		handler := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			resolve := args[0]
			reject := args[1]

			// Create promise.
			go func() {

				out, err := ping(ctx, input)
				fmt.Printf("output %s\n", out)
				if err != nil {
					fmt.Printf("unable to ping %s\n", err)
					errorConstructor := js.Global().Get("Error")
					errorObject := errorConstructor.New(err.Error())
					reject.Invoke(errorObject)
					return
				}
				resolve.Invoke(out)
			}()
			return nil
		})

		promiseConstructor := js.Global().Get("Promise")
		return promiseConstructor.New(handler)
	})
	return jsonFunc
}

func main() {
	fmt.Println("running GO")
	js.Global().Set("ping", pingJS())
	<-make(chan bool)
}
