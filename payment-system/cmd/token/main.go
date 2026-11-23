package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/zfqiang/payment-system/internal/auth"
)

func main() {
	// 该工具只接收一个参数，并将参数作为已验签 JWT 中的用户 ID（sub）。
	if len(os.Args) != 2 {
		log.Fatal("usage: go run ./cmd/token <user-id>")
	}
	token, err := auth.Issue(os.Getenv("JWT_SECRET"), os.Args[1], time.Now().Add(24*time.Hour))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(token)
}
