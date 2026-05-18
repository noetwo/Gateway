package main

import (
	_ "embed"

	"ai-gateway-poller/app"
)

//go:embed html/login.html
var loginHTML string

//go:embed html/index.html
var indexHTML string

func main() {
	app.Run(loginHTML, indexHTML)
}
