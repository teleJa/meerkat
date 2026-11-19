// meerkat-edge 是 Meerkat 的 Edge 程序：注册设备、连接中心并上报指标。
package main

import (
	"os"

	"meerkat/edge/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
