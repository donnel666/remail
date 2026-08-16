package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/donnel666/remail/internal/kitesim"
)

const passwordEnvironment = "KITESIM_PASSWORD"

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	client := kitesim.NewClient(nil)
	if len(args) > 0 && args[0] == "captcha" {
		count := 1
		if len(args) > 1 {
			parsed, err := strconv.Atoi(args[1])
			if err != nil || parsed < 1 || parsed > 20 {
				return fmt.Errorf("captcha count must be between 1 and 20")
			}
			count = parsed
		}
		for range count {
			code, err := client.SolveCaptcha(ctx)
			if err != nil {
				return err
			}
			fmt.Println(code)
		}
		return nil
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: KITESIM_PASSWORD=... kitesim <account> [--sms [phone ...]]\n       kitesim captcha [count]")
	}

	account := args[0]
	password, err := passwordFromEnvironment(os.LookupEnv)
	if err != nil {
		return err
	}
	wantSMS := false
	wantedPhones := map[string]bool{}
	for _, arg := range args[1:] {
		if arg == "--sms" {
			wantSMS = true
			continue
		}
		if !wantSMS {
			return errors.New("unexpected positional argument; set KITESIM_PASSWORD and put phone filters after --sms")
		}
		wantedPhones[strings.TrimSpace(arg)] = true
	}
	token, err := client.Login(ctx, account, password)
	if err != nil {
		return err
	}
	orders, err := client.PhoneOrders(ctx, token)
	if err != nil {
		return err
	}
	fmt.Printf("登录成功，共 %d 个手机号订单\n", len(orders))
	for _, order := range orders {
		fmt.Printf("[%s] %s 订单号=%s 到期=%s\n", order.Status.Label(), order.FullPhoneNumber(), order.OrderNo, order.ExpireTime)
	}
	if !wantSMS {
		return nil
	}
	for _, order := range orders {
		if order.Status != kitesim.PhoneActive || len(wantedPhones) > 0 && !wantedPhones[order.PhoneNumber] {
			continue
		}
		messages, err := client.Messages(ctx, token, string(order.ID), order.PhoneNumber)
		if err != nil {
			return err
		}
		fmt.Printf("\n===== %s 短信列表 =====\n", order.FullPhoneNumber())
		if len(messages) == 0 {
			fmt.Println("(无短信)")
			continue
		}
		for _, message := range messages {
			caller := strings.TrimSpace(message.Caller)
			if caller == "" {
				caller = "?"
			}
			fmt.Printf("[%s] %s -> %s\n", message.Time(), caller, strings.TrimSpace(message.Content))
		}
	}
	return nil
}

func passwordFromEnvironment(lookup func(string) (string, bool)) (string, error) {
	if lookup == nil {
		return "", errors.New("KITESIM_PASSWORD is required")
	}
	password, exists := lookup(passwordEnvironment)
	if !exists || strings.TrimSpace(password) == "" {
		return "", errors.New("KITESIM_PASSWORD is required")
	}
	return password, nil
}
