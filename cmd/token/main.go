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
	token, e := (auth.Auth{Secret: c.JWTSecret, Issuer: c.Issuer, Audience: c.Audience}).Token(*user, *role)
	if e != nil {
		os.Exit(1)
	}
	fmt.Println(token)
}
