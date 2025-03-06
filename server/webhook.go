package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/topi314/gobin/v3/internal/ezhttp"
	"github.com/topi314/gobin/v3/internal/flags"
	"github.com/topi314/gobin/v3/internal/httperr"
	"github.com/topi314/gobin/v3/server/database"
)

var (
	ErrWebhookNotFound            = errors.New("webhook not found")
	ErrMissingWebhookSecret       = errors.New("missing webhook secret")
	ErrMissingWebhookURL          = errors.New("missing webhook url")
	ErrMissingWebhookEvents       = errors.New("missing webhook events")
	ErrMissingURLOrSecretOrEvents = errors.New("missing url, secret or events")
)

type (
	WebhookCreateRequest struct {
		URL    string   `json:"url"`
		Secret string   `json:"secret"`
		Events []string `json:"events"`
	}

	WebhookUpdateRequest struct {
		URL    string   `json:"url"`
		Secret string   `json:"secret"`
		Events []string `json:"events"`
	}

	WebhookResponse struct {
		ID          string   `json:"id"`
		DocumentKey string   `json:"document_key"`
		URL         string   `json:"url"`
		Secret      string   `json:"secret"`
		Events      []string `json:"events"`
	}

	WebhookEventRequest struct {
		WebhookID string          `json:"webhook_id"`
		Event     string          `json:"event"`
		CreatedAt time.Time       `json:"created_at"`
		Document  WebhookDocument `json:"document"`
	}

	WebhookDocument struct {
		Key     string                `json:"key"`
		Version int64                 `json:"version"`
		Files   []WebhookDocumentFile `json:"files"`
	}

	WebhookDocumentFile struct {
		Name      string     `json:"name"`
		Content   string     `json:"content"`
		Language  string     `json:"language"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
)

const (
	WebhookEventUpdate string = "update"
	WebhookEventDelete string = "delete"
)

func (s *Server) ExecuteWebhooks(ctx context.Context, event string, document WebhookDocument) {
	if !s.cfg.Webhook.Enabled {
		return
	}
	s.webhookWaitGroup.Add(1)
	ctx, span := s.tracer.Start(context.WithoutCancel(ctx), "executeWebhooks", trace.WithAttributes(
		attribute.String("event", event),
		attribute.String("document_id", document.Key),
	))
	go func() {
		defer span.End()
		s.executeWebhooks(ctx, event, document)
	}()
}

func (s *Server) executeWebhooks(ctx context.Context, event string, document WebhookDocument) {
	defer s.webhookWaitGroup.Done()

	dbCtx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.Webhook.Timeout))
	defer cancel()

	var (
		webhooks []database.Webhook
		err      error
	)
	if event == WebhookEventDelete {
		webhooks, err = s.db.GetAndDeleteWebhooksByDocumentID(dbCtx, document.Key)
	} else {
		webhooks, err = s.db.GetWebhooksByDocumentID(dbCtx, document.Key)
	}
	if err != nil {
		slog.ErrorContext(dbCtx, "failed to get webhooks by document id", slog.Any("err", err))
		return
	}

	if len(webhooks) == 0 {
		return
	}

	now := time.Now()
	var wg sync.WaitGroup
	for _, webhook := range webhooks {
		if !slices.Contains(strings.Split(webhook.Events, ","), event) {
			continue
		}

		wg.Add(1)
		go func(webhook database.Webhook) {
			defer wg.Done()
			s.executeWebhook(ctx, webhook.URL, webhook.Secret, WebhookEventRequest{
				WebhookID: webhook.ID,
				Event:     event,
				CreatedAt: now,
				Document:  document,
			})
		}(webhook)
	}
	wg.Wait()

	slog.DebugContext(ctx, "finished emitting webhooks", slog.String("event", event), slog.Any("document_id", document.Key))
}

func (s *Server) executeWebhook(ctx context.Context, url string, secret string, request WebhookEventRequest) {
	ctx, span := s.tracer.Start(ctx, "executeWebhook", trace.WithAttributes(
		attribute.String("url", url),
		attribute.String("event", request.Event),
		attribute.String("document_id", request.Document.Key),
	))
	defer span.End()

	logger := slog.Default().With(slog.String("event", request.Event), slog.Any("webhook_id", request.WebhookID), slog.Any("document_id", request.Document.Key))
	logger.DebugContext(ctx, "emitting webhook", slog.String("url", url))

	buff := new(bytes.Buffer)
	if err := json.NewEncoder(buff).Encode(request); err != nil {
		span.SetStatus(codes.Error, "failed to encode document")
		span.RecordError(err)
		logger.ErrorContext(ctx, "failed to encode document", slog.Any("err", err))
		return
	}

	rq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, buff)
	if err != nil {
		span.SetStatus(codes.Error, "failed to create request")
		span.RecordError(err)
		logger.ErrorContext(ctx, "failed to create request", slog.Any("err", err))
		return
	}
	rq.Header.Add(ezhttp.HeaderContentType, ezhttp.ContentTypeJSON)
	rq.Header.Add(ezhttp.HeaderUserAgent, fmt.Sprintf("gobin/%s", s.version))
	rq.Header.Add(ezhttp.HeaderAuthorization, fmt.Sprintf("Secret %s", secret))

	for i := 0; i < s.cfg.Webhook.MaxTries; i++ {
		backoff := time.Duration(s.cfg.Webhook.BackoffFactor * float64(s.cfg.Webhook.Backoff) * float64(i))
		if backoff > time.Nanosecond {
			if backoff > time.Duration(s.cfg.Webhook.MaxBackoff) {
				backoff = time.Duration(s.cfg.Webhook.MaxBackoff)
			}
			logger.DebugContext(ctx, "sleeping backoff", slog.Duration("backoff", backoff))
			time.Sleep(backoff)
		}

		rs, err := s.client.Do(rq)
		if err != nil {
			logger.DebugContext(ctx, "failed to execute request", slog.Any("err", err))
			continue
		}

		if rs.StatusCode < 200 || rs.StatusCode >= 300 {
			logger.DebugContext(ctx, "invalid status code", slog.Int("status", rs.StatusCode))
			continue
		}

		logger.DebugContext(ctx, "successfully executed webhook", slog.String("status", rs.Status))
		return
	}

	err = errors.New("max tries reached")
	span.SetStatus(codes.Error, "failed to execute webhook")
	span.RecordError(err)
	logger.ErrorContext(ctx, "failed to execute webhook", slog.Any("err", err))
}

func (s *Server) PostDocumentWebhook(w http.ResponseWriter, r *http.Request) {
	documentID := chi.URLParam(r, "documentID")

	var webhookCreate WebhookCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&webhookCreate); err != nil {
		s.error(w, r, httperr.BadRequest(err))
		return
	}

	if webhookCreate.URL == "" {
		s.error(w, r, httperr.BadRequest(ErrMissingWebhookURL))
		return
	}

	if webhookCreate.Secret == "" {
		s.error(w, r, httperr.BadRequest(ErrMissingWebhookSecret))
		return
	}

	if len(webhookCreate.Events) == 0 {
		s.error(w, r, httperr.BadRequest(ErrMissingWebhookEvents))
		return
	}

	claims := GetClaims(r)
	if flags.Misses(claims.Permissions, PermissionWebhook) {
		s.error(w, r, httperr.Forbidden(ErrPermissionDenied("webhook")))
		return
	}

	webhook, err := s.db.CreateWebhook(r.Context(), documentID, webhookCreate.URL, webhookCreate.Secret, webhookCreate.Events)
	if err != nil {
		s.error(w, r, err)
		return
	}

	s.ok(w, r, WebhookResponse{
		ID:          webhook.ID,
		DocumentKey: webhook.DocumentID,
		URL:         webhook.URL,
		Secret:      webhook.Secret,
		Events:      strings.Split(webhook.Events, ","),
	})
}

func (s *Server) GetDocumentWebhook(w http.ResponseWriter, r *http.Request) {
	documentID := chi.URLParam(r, "documentID")
	webhookID := chi.URLParam(r, "webhookID")
	secret := GetWebhookSecret(r)
	if secret == "" {
		s.error(w, r, httperr.BadRequest(ErrMissingWebhookSecret))
		return
	}

	webhook, err := s.db.GetWebhook(r.Context(), documentID, webhookID, secret)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.error(w, r, httperr.NotFound(ErrWebhookNotFound))
			return
		}
		s.error(w, r, err)
		return
	}

	s.ok(w, r, WebhookResponse{
		ID:          webhook.ID,
		DocumentKey: webhook.DocumentID,
		URL:         webhook.URL,
		Secret:      webhook.Secret,
		Events:      strings.Split(webhook.Events, ","),
	})
}

func (s *Server) PatchDocumentWebhook(w http.ResponseWriter, r *http.Request) {
	documentID := chi.URLParam(r, "documentID")
	webhookID := chi.URLParam(r, "webhookID")
	secret := GetWebhookSecret(r)
	if secret == "" {
		s.error(w, r, httperr.BadRequest(ErrMissingWebhookSecret))
		return
	}

	var webhookUpdate WebhookUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&webhookUpdate); err != nil {
		s.error(w, r, httperr.BadRequest(err))
		return
	}

	if webhookUpdate.URL == "" && webhookUpdate.Secret == "" && len(webhookUpdate.Events) == 0 {
		s.error(w, r, httperr.BadRequest(ErrMissingURLOrSecretOrEvents))
		return
	}

	webhook, err := s.db.UpdateWebhook(r.Context(), documentID, webhookID, secret, webhookUpdate.URL, webhookUpdate.Secret, webhookUpdate.Events)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.error(w, r, httperr.NotFound(ErrWebhookNotFound))
			return
		}
		s.error(w, r, err)
		return
	}

	s.ok(w, r, WebhookResponse{
		ID:          webhook.ID,
		DocumentKey: webhook.DocumentID,
		URL:         webhook.URL,
		Secret:      webhook.Secret,
		Events:      strings.Split(webhook.Events, ","),
	})
}

func (s *Server) DeleteDocumentWebhook(w http.ResponseWriter, r *http.Request) {
	documentID := chi.URLParam(r, "documentID")
	webhookID := chi.URLParam(r, "webhookID")
	secret := GetWebhookSecret(r)
	if secret == "" {
		s.error(w, r, httperr.BadRequest(ErrMissingWebhookSecret))
		return
	}

	if err := s.db.DeleteWebhook(r.Context(), documentID, webhookID, secret); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.error(w, r, httperr.NotFound(ErrWebhookNotFound))
			return
		}
		s.error(w, r, err)
		return
	}

	s.ok(w, r, nil)
}

func GetWebhookSecret(r *http.Request) string {
	secretStr := r.Header.Get(ezhttp.HeaderAuthorization)
	if len(secretStr) > 7 && strings.ToUpper(secretStr[0:6]) == "SECRET" {
		return secretStr[7:]
	}
	return ""
}
