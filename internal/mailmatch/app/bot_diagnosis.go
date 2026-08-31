package app

import (
	"context"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
)

const botDiagnosisPickupGrace = time.Minute

type CodeDiagnosisOrderFact struct {
	OrderNo                  string
	ServiceMode              string
	Status                   string
	EmailResourceID          uint
	DeliveryStoredAt         *time.Time
	ResourceAbnormalRefunded bool
}

type CodeDiagnosisLookup struct {
	EmailOrderExists bool
	Orders           []CodeDiagnosisOrderFact
}

type CodeDiagnosisRepository interface {
	LookupCodeDiagnosis(ctx context.Context, userID uint, email string, projectID uint) (CodeDiagnosisLookup, error)
}

type CodeDiagnosisRefreshPort interface {
	RefreshCodeDiagnosis(ctx context.Context, orderNo, email string, emailResourceID uint) error
}

type BotCodeDiagnosis struct {
	Result string
	Reason string
	Action string
}

type BotDiagnosisService struct {
	repo    CodeDiagnosisRepository
	refresh CodeDiagnosisRefreshPort
	now     func() time.Time
}

func NewBotDiagnosisService(repo CodeDiagnosisRepository, refresh ...CodeDiagnosisRefreshPort) *BotDiagnosisService {
	service := &BotDiagnosisService{repo: repo, now: func() time.Time { return time.Now().UTC() }}
	if len(refresh) > 0 {
		service.refresh = refresh[0]
	}
	return service
}

func (s *BotDiagnosisService) SetRefresh(port CodeDiagnosisRefreshPort) {
	if s != nil {
		s.refresh = port
	}
}

func (s *BotDiagnosisService) DiagnoseCode(ctx context.Context, userID uint, email string, projectID uint) (BotCodeDiagnosis, error) {
	email = normalizeEmail(email)
	if s == nil || s.repo == nil || userID == 0 || projectID == 0 || email == "" {
		return BotCodeDiagnosis{}, domain.ErrInvalidRequest
	}
	lookup, err := s.repo.LookupCodeDiagnosis(ctx, userID, email, projectID)
	if err != nil {
		return BotCodeDiagnosis{}, err
	}
	order, result := selectCodeDiagnosisOrder(lookup)
	if result != "" {
		return botDiagnosis(result), nil
	}
	if result = classifyCodeDiagnosisOrder(order, s.now().UTC()); result != "" {
		return botDiagnosis(result), nil
	}
	if s.refresh != nil && strings.TrimSpace(order.Status) == "active" {
		refreshErr := s.refresh.RefreshCodeDiagnosis(ctx, order.OrderNo, email, order.EmailResourceID)
		refreshed, lookupErr := s.repo.LookupCodeDiagnosis(ctx, userID, email, projectID)
		if lookupErr != nil {
			return BotCodeDiagnosis{}, lookupErr
		}
		if refreshedOrder, refreshedResult := selectCodeDiagnosisOrder(refreshed); refreshedResult == "" {
			order = refreshedOrder
			if classified := classifyCodeDiagnosisOrder(order, s.now().UTC()); classified != "" {
				return botDiagnosis(classified), nil
			}
		}
		if refreshErr != nil {
			return BotCodeDiagnosis{}, refreshErr
		}
	}
	return botDiagnosis("cause_not_confirmed"), nil
}

func selectCodeDiagnosisOrder(lookup CodeDiagnosisLookup) (CodeDiagnosisOrderFact, string) {
	if len(lookup.Orders) == 0 {
		if lookup.EmailOrderExists {
			return CodeDiagnosisOrderFact{}, "project_mismatch"
		}
		return CodeDiagnosisOrderFact{}, "order_not_found"
	}
	for _, order := range lookup.Orders {
		if order.Status == "active" || order.Status == "paid" || order.Status == "pending_payment" {
			return order, ""
		}
	}
	return lookup.Orders[0], ""
}

func classifyCodeDiagnosisOrder(order CodeDiagnosisOrderFact, now time.Time) string {
	if order.ServiceMode != "code" {
		return "project_mismatch"
	}
	if order.ResourceAbnormalRefunded {
		return "resource_abnormal_refunded"
	}
	if order.DeliveryStoredAt != nil && !order.DeliveryStoredAt.IsZero() {
		if !order.DeliveryStoredAt.UTC().Add(botDiagnosisPickupGrace).After(now) {
			return "pickup_not_requested"
		}
		return "pickup_grace_period"
	}
	return ""
}

func botDiagnosis(result string) BotCodeDiagnosis {
	messages := map[string][2]string{
		"order_not_found":            {"未找到当前账号下与该邮箱对应的接码订单。", "请核对邮箱后重试。"},
		"project_mismatch":           {"提供的项目与该邮箱对应的订单不一致。", "请选择下单时使用的项目后重试。"},
		"pickup_not_requested":       {"验证码邮件已匹配超过 1 分钟，但用户端尚未正确拉取。", "请在工作台查看订单，或使用该订单的正确邮箱和凭证调用 pickup。"},
		"resource_abnormal_refunded": {"邮箱资源异常，系统已自动退款。", "请在工作台查看退款记录后重新下单。"},
		"pickup_grace_period":        {"验证码邮件刚刚匹配，仍在 1 分钟拉取宽限期内。", "请立即使用正确邮箱和凭证调用 pickup。"},
		"cause_not_confirmed":        {"已检查本地邮件缓存并触发一次拉取，暂未确认上述三类原因。", "请稍后重试；持续无结果时联系人工客服。"},
	}
	message, ok := messages[result]
	if !ok {
		result = "cause_not_confirmed"
		message = messages[result]
	}
	return BotCodeDiagnosis{Result: result, Reason: message[0], Action: message[1]}
}
