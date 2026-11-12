package main

import (
	"flag"
	"fmt"
	"github.com/nemo-crypto/pay/internal/auth"
	"github.com/nemo-crypto/pay/internal/config"
	"os"
)

func main() {
	user := flag.String("user", "demo-user", "受信任的用户 ID")
	role := flag.String("role", "user", "user 或 admin")
	wechatApp := flag.String("wechat-app-id", "", "受信任身份绑定的微信 AppID")
	wechatOpenID := flag.String("wechat-openid", "", "仅填写由微信登录服务端交换得到的 OpenID")
	flag.Parse()
	c, e := config.Load()
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if *role != "user" && *role != "admin" {
		fmt.Fprintln(os.Stderr, "invalid role")
		os.Exit(1)
	}
	if (*wechatApp == "") != (*wechatOpenID == "") {
		fmt.Fprintln(os.Stderr, "wechat-app-id and wechat-openid must be supplied together")
		os.Exit(1)
	}
	ids := map[string]string{}
	if *wechatApp != "" {
		ids[*wechatApp] = *wechatOpenID
	}
	token, e := (auth.Auth{Secret: c.JWTSecret, Issuer: c.Issuer, Audience: c.Audience}).TokenWithWechat(*user, *role, ids)
	if e != nil {
		os.Exit(1)
	}
	fmt.Println(token)
}
