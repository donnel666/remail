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
	ProjectID                uint
	ProjectName              string
	ServiceMode              string
	Status                   string
	EmailResourceID          uint
	DeliveryStoredAt         *time.Time
	ResourceAbnormalRefunded bool
}

type CodeDiagnosisLookup struct {
	Orders []CodeDiagnosisOrderFact
}

type CodeDiagnosisRepository interface {
	LookupCodeDiagnosis(ctx context.Context, userID uint, email string) (CodeDiagnosisLookup, error)
}

type CodeDiagnosisRefreshPort interface {
	RefreshCodeDiagnosis(ctx context.Context, orderNo, email string, emailResourceID uint) (CodeDiagnosisRefreshResult, error)
}

type CodeDiagnosisRefreshResult struct {
	DeliveryFound bool
	ReceivedAt    time.Time
}

type BotCodeDiagnosis struct {
	Result      string
	Reason      string
	Action      string
	ProjectID   uint
	ProjectName string
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

func (s *BotDiagnosisService) DiagnoseCode(ctx context.Context, userID uint, email string) (BotCodeDiagnosis, error) {
	email = normalizeEmail(email)
	if s == nil || s.repo == nil || userID == 0 || email == "" {
		return BotCodeDiagnosis{}, domain.ErrInvalidRequest
	}
	lookup, err := s.repo.LookupCodeDiagnosis(ctx, userID, email)
	if err != nil {
		return BotCodeDiagnosis{}, err
	}
	order, result := selectCodeDiagnosisOrder(lookup)
	if result != "" {
		return botDiagnosis(result, order), nil
	}
	if result = classifyCodeDiagnosisOrder(order, s.now().UTC()); result != "" {
		return botDiagnosis(result, order), nil
	}
	status := strings.TrimSpace(order.Status)
	if s.refresh != nil && (status == "active" || status == "completed") {
		refreshResult, refreshErr := s.refresh.RefreshCodeDiagnosis(ctx, order.OrderNo, email, order.EmailResourceID)
		refreshed, lookupErr := s.repo.LookupCodeDiagnosis(ctx, userID, email)
		if lookupErr != nil {
			return BotCodeDiagnosis{}, lookupErr
		}
		if refreshedOrder, refreshedResult := selectCodeDiagnosisOrder(refreshed); refreshedResult == "" {
			order = refreshedOrder
			if classified := classifyCodeDiagnosisOrder(order, s.now().UTC()); classified != "" {
				return botDiagnosis(classified, order), nil
			}
		}
		if refreshResult.DeliveryFound {
			receivedAt := refreshResult.ReceivedAt.UTC()
			if receivedAt.IsZero() {
				receivedAt = s.now().UTC()
			}
			order.DeliveryStoredAt = &receivedAt
			if classified := classifyCodeDiagnosisOrder(order, s.now().UTC()); classified != "" {
				return botDiagnosis(classified, order), nil
			}
		}
		if refreshErr != nil {
			return BotCodeDiagnosis{}, refreshErr
		}
	}
	return botDiagnosis("cause_not_confirmed", order), nil
}

func selectCodeDiagnosisOrder(lookup CodeDiagnosisLookup) (CodeDiagnosisOrderFact, string) {
	if len(lookup.Orders) == 0 {
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

func botDiagnosis(result string, order CodeDiagnosisOrderFact) BotCodeDiagnosis {
	messages := map[string][2]string{
		"order_not_found":            {"当前账号下没有找到该邮箱对应的订单。", "请确认邮箱后重试。"},
		"pickup_not_requested":       {"验证码邮件已经到达，但尚未完成领取。", "请回到对应订单重新获取验证码。"},
		"resource_abnormal_refunded": {"邮箱资源异常，系统已自动退款。", "请在工作台查看退款记录后重新下单。"},
		"pickup_grace_period":        {"验证码正在处理中。", "请稍后重新获取。"},
		"cause_not_confirmed":        {"暂未发现明确异常。", "请稍后重试；持续无结果时联系人工客服。"},
	}
	message, ok := messages[result]
	if !ok {
		result = "cause_not_confirmed"
		message = messages[result]
	}
	return BotCodeDiagnosis{
		Result: result, Reason: message[0], Action: message[1],
		ProjectID: order.ProjectID, ProjectName: order.ProjectName,
	}
}
