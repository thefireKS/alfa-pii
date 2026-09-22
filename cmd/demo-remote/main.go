// Command demo-remote runs the masking -> LLM -> restore demonstration against
// a running pii-service instance over its real /v1/mask and /v1/restore
// endpoints. It reuses the deterministic in-process demo model, so no real
// network or provider key is required. The consumer secret is read from the
// environment and is never printed.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"alfa-hackathon.local/pii/internal/demo"
)

func main() {
	baseURL := flag.String("base", "http://127.0.0.1:8080", "base URL of the running service")
	secretEnv := flag.String("secret-env", "PII_CONSUMER_DEMO_SECRET", "environment variable holding the demo consumer secret")
	flag.Parse()

	secret := os.Getenv(*secretEnv)
	if secret == "" {
		fmt.Fprintf(os.Stderr, "secret environment variable %q is empty\n", *secretEnv)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := demo.NewClient(*baseURL, secret)
	runner := demo.NewRunner(client, client, demo.DemoModel{})

	original := "ФИО: Иванов Иван Иванович, телефон +7 900 123-45-67, email ivan@example.ru, паспорт 4500 123456."
	res, err := runner.Run(ctx, "demo-remote-1", original)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка защищённого сценария:", err)
		os.Exit(1)
	}

	fmt.Println("=== Демонстрация защиты персональных данных (против запущенного сервиса) ===")
	fmt.Println("Синтетические данные; реальная сеть и ключ провайдера не используются.")
	fmt.Println()
	fmt.Println("--- Защищённый сценарий (потребитель demo) ---")
	fmt.Println("Исходный текст :", res.Original)
	fmt.Println("Маска          :", res.Masked)
	fmt.Println("Ответ модели   :", res.ModelResponse)
	fmt.Println("Восстановлено  :", res.Restored)
	fmt.Println("Модель получила маску, а не исходные значения:",
		!containsAny(res.Masked, "Иванов", "900 123-45-67", "ivan@example.ru", "4500 123456"))
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}