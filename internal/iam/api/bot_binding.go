package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/iam/app"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const (
	botBindingReadLimit       = 20
	botBindingMutationLimit   = 5
	botBindingReadWindow      = 60
	botBindingMutationWindow  = 15 * 60
	botBindingKeyLimit        = 120
	botBindingRequestMaxBytes = 4 << 10
)

type botBindingRequest struct {
	Email    string `json:"email" binding:"required,email,max=320"`
	Password string `json:"password" binding:"required,max=1024"`
}

type botBindingResponse struct {
	Result         string `json:"result"`
	Reason         string `json:"reason"`
	AccountDisplay string `json:"accountDisplay,omitempty"`
}

type botBindingHandler struct {
	bindings *app.BotBindingUseCase
	limiter  botBindingLimiter
}

type botBindingLimiter interface {
	TakeBotBinding(ctx context.Context, email string) (int, error)
	CancelBotBinding(ctx context.Context, email string) error
	CompleteBotBinding(ctx context.Context, email string) error
}

// RegisterBotBindingRoutes mounts the platform-neutral binding endpoints on a
// /v1/bot group. The caller must put middleware.BotSystemKeyRequired on rg.
func RegisterBotBindingRoutes(rg *gin.RouterGroup, mod *IAMModule, rdb redis.UniversalClient) {
	h := &botBindingHandler{}
	if mod != nil {
		h.bindings = mod.BotBindingUseCase
		h.limiter, _ = mod.AbuseLimiter.(botBindingLimiter)
	}
	identity := middleware.BotIdentityRequired()
	private := middleware.BotPrivateRequired()
	rg.POST("/bindings", identity, private,
		middleware.RateLimitPerSystemKey(rdb, "bot_binding_create", botBindingKeyLimit, 60),
		middleware.RateLimitPerBotSubject(rdb, "binding_create", botBindingMutationLimit, botBindingMutationWindow), h.post)
	rg.GET("/binding", identity, private,
		middleware.RateLimitPerSystemKey(rdb, "bot_binding_read", botBindingKeyLimit, 60),
		middleware.RateLimitPerBotSubject(rdb, "binding_read", botBindingReadLimit, botBindingReadWindow), h.get)
	rg.DELETE("/binding", identity, private,
		middleware.RateLimitPerSystemKey(rdb, "bot_binding_delete", botBindingKeyLimit, 60),
		middleware.RateLimitPerBotSubject(rdb, "binding_delete", botBindingMutationLimit, botBindingMutationWindow), h.delete)
}

func (h *botBindingHandler) post(c *gin.Context) {
	identity, ok := middleware.GetCurrentBotIdentity(c)
	if !ok || h == nil || h.bindings == nil {
		writeBotBindingError(c, domain.ErrThirdPartyIdentityUnavailable)
		return
	}
	var req botBindingRequest
	if err := bindBotBindingJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, botBindingResponse{
			Result: "invalid_request", Reason: "Invalid request body.",
		})
		return
	}
	if h.limiter == nil {
		writeBotBindingError(c, errors.New("bot binding limiter unavailable"))
		return
	}
	retryAfter, err := h.limiter.TakeBotBinding(c.Request.Context(), req.Email)
	if err != nil {
		writeBotBindingError(c, err)
		return
	}
	if retryAfter > 0 {
		writeTooManyRequests(c, retryAfter)
		return
	}
	info, err := h.bindings.Bind(c.Request.Context(), identity.Platform, identity.SubjectNamespace, identity.Subject, req.Email, req.Password)
	if err != nil {
		if !errors.Is(err, domain.ErrAccountOrPasswordIncorrect) {
			if limitErr := h.limiter.CancelBotBinding(c.Request.Context(), req.Email); limitErr != nil {
				writeBotBindingError(c, limitErr)
				return
			}
		}
		writeBotBindingError(c, err)
		return
	}
	if err := h.limiter.CompleteBotBinding(c.Request.Context(), req.Email); err != nil {
		slog.Warn("clear bot binding abuse limit", "request_id", middleware.GetRequestID(c), "error", err)
	}
	c.JSON(http.StatusOK, botBindingResponse{
		Result: "bound", Reason: "The current bot account is bound to remail.",
		AccountDisplay: info.MaskedEmail,
	})
}

func bindBotBindingJSON(c *gin.Context, destination any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, botBindingRequestMaxBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func (h *botBindingHandler) get(c *gin.Context) {
	identity, ok := middleware.GetCurrentBotIdentity(c)
	if !ok || h == nil || h.bindings == nil {
		writeBotBindingError(c, domain.ErrThirdPartyIdentityUnavailable)
		return
	}
	info, err := h.bindings.Get(c.Request.Context(), identity.Platform, identity.SubjectNamespace, identity.Subject)
	if err != nil {
		writeBotBindingError(c, err)
		return
	}
	response := botBindingResponse{Result: "unbound", Reason: "The current bot account is not bound to remail."}
	if info.Bound && !info.Available {
		response.Result = "account_unavailable"
		response.Reason = "The bound remail account is unavailable."
	} else if info.Bound {
		response.Result = "bound"
		response.Reason = "The current bot account is bound to remail."
		response.AccountDisplay = info.MaskedEmail
	}
	c.JSON(http.StatusOK, response)
}

func (h *botBindingHandler) delete(c *gin.Context) {
	identity, ok := middleware.GetCurrentBotIdentity(c)
	if !ok || h == nil || h.bindings == nil {
		writeBotBindingError(c, domain.ErrThirdPartyIdentityUnavailable)
		return
	}
	if err := h.bindings.Unbind(c.Request.Context(), identity.Platform, identity.SubjectNamespace, identity.Subject); err != nil {
		writeBotBindingError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func writeBotBindingError(c *gin.Context, err error) {
	response := botBindingResponse{Result: "service_unavailable", Reason: "Service is temporarily unavailable."}
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, domain.ErrAccountOrPasswordIncorrect):
		status, response.Result, response.Reason = http.StatusUnprocessableEntity, "credential_incorrect", "Account or password is incorrect."
	case errors.Is(err, domain.ErrThirdPartyIdentityAlreadyBound):
		status, response.Result, response.Reason = http.StatusConflict, "binding_conflict", "The bot account or remail account already has another binding."
	case errors.Is(err, domain.ErrThirdPartyIdentityUnavailable):
		status, response.Result, response.Reason = http.StatusUnauthorized, "bot_identity_required", "Bot identity is required."
	default:
		slog.Error("bot binding failed", "request_id", middleware.GetRequestID(c), "error", err)
	}
	c.JSON(status, response)
}
