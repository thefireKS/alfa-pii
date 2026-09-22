// Command demo runs a reproducible end-to-end demonstration of the masking ->
// LLM -> restore cycle over the existing /v1/mask and /v1/restore endpoints.
// It uses a dedicated demo consumer and a deterministic in-process model, so no
// real network or provider key is required. The authentication secret is never
// printed, and the original text is never sent to the model.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"alfa-hackathon.local/pii/internal/app"
	"alfa-hackathon.local/pii/internal/demo"
	"alfa-hackathon.local/pii/internal/httpapi"
	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/recognizer"
	"alfa-hackathon.local/pii/internal/store"
)

// demoConsumer is the dedicated consumer for the protected scenario. It uses
// the distinguishable marker format so markers can be reordered, repeated and
// dropped inside the model answer.
func demoConsumer() app.Consumer {
	return app.Consumer{
		Name:           "demo",
		Enabled:        true,
		Types:          []recognizer.Type{recognizer.FullName, recognizer.Phone, recognizer.Email, recognizer.Passport},
		MaskingEnabled: true,
		CanRestore:     true,
		MaskFormat:     app.FormatMarker,
	}
}

// noRestoreConsumer can mask but lacks the restore right.
func noRestoreConsumer() app.Consumer {
	return app.Consumer{
		Name:           "norestore",
		Enabled:        true,
		Types:          []recognizer.Type{recognizer.Email},
		MaskingEnabled: true,
		CanRestore:     false,
		MaskFormat:     app.FormatMarker,
	}
}

// noMaskConsumer has masking disabled. It is shown as a separate, explicitly
// not-protected scenario.
func noMaskConsumer() app.Consumer {
	return app.Consumer{
		Name:           "nomask",
		Enabled:        true,
		Types:          []recognizer.Type{recognizer.Email},
		MaskingEnabled: false,
		CanRestore:     true,
		MaskFormat:     app.FormatMarker,
	}
}

// boomConsumer masks a type whose recognizer is overridden to fail, so the
// masking-error protection can be demonstrated through the real endpoint.
func boomConsumer() app.Consumer {
	return app.Consumer{
		Name:           "boom",
		Enabled:        true,
		Types:          []recognizer.Type{recognizer.Email},
		MaskingEnabled: true,
		CanRestore:     true,
		MaskFormat:     app.FormatMarker,
	}
}

// failingRecognizer returns an error for every Find call, simulating an
// internal recognition failure that must fail the operation closed.
type failingRecognizer struct{ typ recognizer.Type }

func (f failingRecognizer) Type() recognizer.Type { return f.typ }

func (f failingRecognizer) Find(string) ([]recognizer.Fragment, error) {
	return nil, errors.New("recognition failure")
}

// failingRegistry wraps a real registry and overrides one type with a failing
// recognizer, so only the boom consumer is affected.
type failingRegistry struct {
	base     *recognizer.Registry
	failType recognizer.Type
}

func (r *failingRegistry) Recognizers(types []recognizer.Type) ([]recognizer.Recognizer, func(recognizer.Type) int, error) {
	recs, prio, err := r.base.Recognizers(types)
	if err != nil {
		return nil, nil, err
	}
	for i, rec := range recs {
		if rec.Type() == r.failType {
			recs[i] = failingRecognizer{typ: r.failType}
		}
	}
	return recs, prio, nil
}

// recordingModel counts calls so the demo can prove the model is not invoked
// when masking fails.
type recordingModel struct {
	model demo.Model
	calls int
}

func (m *recordingModel) Complete(ctx context.Context, prompt string) (string, error) {
	m.calls++
	return m.model.Complete(ctx, prompt)
}

func main() {
	// Main service: the protected demo consumer plus the policy scenarios.
	reg := recognizer.NewRegistry()
	consumers := []app.Consumer{demoConsumer(), noRestoreConsumer(), noMaskConsumer()}
	secrets := map[string]string{
		"demo-secret":      "demo",
		"norestore-secret": "norestore",
		"nomask-secret":    "nomask",
	}
	mainSvc, err := buildService(reg, consumers)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build main service:", err)
		os.Exit(1)
	}
	mainServer := httptest.NewServer(httpapi.NewHandler(mainSvc, httpapi.NewAuthenticator(secrets), func() bool { return true }, 10, 1<<20).Routes())
	defer mainServer.Close()

	// Boom service: the same registry with the email recognizer overridden to
	// fail, so masking always errors for the boom consumer.
	boomReg := &failingRegistry{base: recognizer.NewRegistry(), failType: recognizer.Email}
	boomSvc, err := buildService(boomReg, []app.Consumer{boomConsumer()})
	if err != nil {
		fmt.Fprintln(os.Stderr, "build boom service:", err)
		os.Exit(1)
	}
	boomServer := httptest.NewServer(httpapi.NewHandler(boomSvc, httpapi.NewAuthenticator(map[string]string{"boom-secret": "boom"}), func() bool { return true }, 10, 1<<20).Routes())
	defer boomServer.Close()

	ctx := context.Background()

	fmt.Println("=== Демонстрация защиты персональных данных ===")
	fmt.Println("Синтетические данные; реальная сеть и ключ провайдера не используются.")
	fmt.Println()

	runProtected(ctx, mainServer.URL)
	fmt.Println()
	runMaskingError(ctx, boomServer.URL)
	fmt.Println()
	runRestoreDenied(ctx, mainServer.URL)
	fmt.Println()
	runMaskingDisabled(ctx, mainServer.URL)
}

// buildService wires the store, application and HTTP handler for the given
// registry and consumers.
func buildService(reg app.Registry, consumers []app.Consumer) (*app.Service, error) {
	processTypes := []recognizer.Type{
		recognizer.Email, recognizer.Phone, recognizer.INN, recognizer.Card,
		recognizer.Passport, recognizer.DepartmentCode, recognizer.DriverLicense,
		recognizer.PIN, recognizer.CVV, recognizer.FullName, recognizer.BirthDate,
		recognizer.BirthPlace, recognizer.Citizenship, recognizer.PassportAuthority,
		recognizer.PassportIssueDate, recognizer.Address, recognizer.CardHolderName,
	}
	m := masker.New("PII")
	st := store.NewMemory(store.Limits{
		MaxEntries:      1000,
		MaxBytes:        1 << 20,
		MaxRecordBytes:  1 << 20,
		TTL:             24 * time.Hour,
		CreateWait:      time.Second,
		CleanupInterval: time.Minute,
	})
	st.StartCleanup()
	svc, err := app.NewManaged(reg, processTypes, consumers, st, m)
	if err != nil {
		return nil, err
	}
	return svc, nil
}

// runProtected runs the main happy-path scenario and prints the cycle.
func runProtected(ctx context.Context, baseURL string) {
	client := demo.NewClient(baseURL, "demo-secret")
	runner := demo.NewRunner(client, client, demo.DemoModel{})

	original := "ФИО: Иванов Иван Иванович, телефон +7 900 123-45-67, email ivan@example.ru, паспорт 4500 123456."
	res, err := runner.Run(ctx, "demo-1", original)
	if err != nil {
		fmt.Println("Ошибка защищённого сценария:", err)
		return
	}
	fmt.Println("--- Защищённый сценарий (потребитель demo) ---")
	fmt.Println("Исходный текст :", res.Original)
	fmt.Println("Маска          :", res.Masked)
	fmt.Println("Ответ модели   :", res.ModelResponse)
	fmt.Println("Восстановлено  :", res.Restored)
	fmt.Println("Модель получила маску, а не исходные значения:", !containsAny(res.Masked, "Иванов", "900 123-45-67", "ivan@example.ru", "4500 123456"))
}

// runMaskingError shows that when masking fails, the model is never called.
func runMaskingError(ctx context.Context, baseURL string) {
	client := demo.NewClient(baseURL, "boom-secret")
	model := &recordingModel{model: demo.DemoModel{}}
	runner := demo.NewRunner(client, client, model)

	_, err := runner.Run(ctx, "boom-1", "mail a@b.ru")
	fmt.Println("--- Защита при ошибке маскирования (потребитель boom) ---")
	fmt.Println("Ошибка маскирования:", err)
	fmt.Println("Вызовов модели     :", model.calls, "(должно быть 0)")
}

// runRestoreDenied shows that a consumer without the restore right cannot
// restore values.
func runRestoreDenied(ctx context.Context, baseURL string) {
	client := demo.NewClient(baseURL, "norestore-secret")
	masked, err := client.Mask(ctx, "nr-1", "mail a@b.ru")
	if err != nil {
		fmt.Println("Ошибка маскирования norestore:", err)
		return
	}
	_, err = client.Restore(ctx, "nr-1", masked)
	fmt.Println("--- Отказ в праве восстановления (потребитель norestore) ---")
	fmt.Println("Маска создана:", masked)
	fmt.Println("Ошибка восстановления:", err)
}

// runMaskingDisabled shows a separate, explicitly not-protected scenario: the
// consumer has masking disabled, so it cannot be used for protected masking.
func runMaskingDisabled(ctx context.Context, baseURL string) {
	client := demo.NewClient(baseURL, "nomask-secret")
	_, err := client.Mask(ctx, "nm-1", "mail a@b.ru")
	fmt.Println("--- Маскирование выключено (потребитель nomask) ---")
	fmt.Println("Этот режим НЕ защищён: маскирование отключено.")
	fmt.Println("Ошибка маскирования:", err)
}

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
