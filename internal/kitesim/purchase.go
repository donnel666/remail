package kitesim

import (
	"context"
	"errors"
	"fmt"
	"strings"

	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"gorm.io/gorm"
)

func (s *Service) executePurchase(ctx context.Context, operation operationModel) (int, []string, error) {
	account, product, err := s.operationAccountAndProduct(ctx, operation)
	if err != nil {
		return 0, nil, err
	}
	completed := 0
	orderNos := make([]string, 0, operation.RequestedCount)
	refs := operationProviderRefs{}
	localCounts, err := s.localNumberSegmentCounts(ctx, operation.CountryCode)
	if err != nil {
		return 0, nil, err
	}
	err = s.withSingleUpstreamClient(ctx, account.Account, proxydomain.ProxyPurposeAuth, func(client *Client) error {
		token, err := s.authenticateOperationClient(ctx, client, account)
		if err != nil {
			return err
		}
		for completed < operation.RequestedCount {
			numbers, err := client.PhoneNumbers(ctx, token, operation.CountryCode)
			if err != nil {
				return err
			}
			selectedNumber, err := pickBestNumber(numbers, localCounts)
			if err != nil {
				return err
			}
			packages, err := client.NumberPackages(ctx, token, operation.CountryCode, selectedNumber.PhoneNumber)
			if err != nil {
				return err
			}
			selectedPackage, err := selectOperationPackage(packages, product)
			if err != nil {
				return err
			}
			if priceExceeds(string(selectedPackage.BuyPrice), operation.Amount) {
				return ErrPriceChanged
			}
			orderNo, err := client.CreatePhoneOrder(
				ctx,
				token,
				operation.CountryCode,
				selectedNumber.PhoneNumber,
				string(selectedPackage.ID),
			)
			if err != nil {
				return err
			}
			orderNos = append(orderNos, orderNo)
			refs.OrderNos = append(refs.OrderNos, orderNo)
			if err := s.recordOperationProgress(ctx, operation.ID, completed, refs); err != nil {
				return err
			}
			if err := confirmPhoneOrderOnce(ctx, client, token, orderNo); err != nil {
				return err
			}
			completed++
			if err := s.recordOperationProgress(ctx, operation.ID, completed, refs); err != nil {
				return errors.Join(ErrPaymentUncertain, err)
			}
			localCounts[numberSegment(selectedNumber)]++
		}
		return nil
	})
	return completed, orderNos, err
}

func (s *Service) localNumberSegmentCounts(ctx context.Context, countryCode string) (map[string]int, error) {
	var phones []phoneModel
	if err := s.db.WithContext(ctx).
		Select("phone_code", "phone_number").
		Where("UPPER(country_code) = ?", strings.ToUpper(strings.TrimSpace(countryCode))).
		Find(&phones).Error; err != nil {
		return nil, fmt.Errorf("load Kitesim local phone inventory: %w", err)
	}
	counts := make(map[string]int, len(phones))
	for _, phone := range phones {
		segment := numberSegment(PhoneNumberOffer{PhoneCode: stringValue(phone.PhoneCode), PhoneNumber: phone.PhoneNumber})
		if segment != "" {
			counts[segment]++
		}
	}
	return counts, nil
}

func (s *Service) executeRenewal(ctx context.Context, operation operationModel) (string, error) {
	if operation.PhoneID == nil || *operation.PhoneID == 0 {
		return "", ErrPhoneMissing
	}
	account, product, err := s.operationAccountAndProduct(ctx, operation)
	if err != nil {
		return "", err
	}
	var phone phoneModel
	if err := s.db.WithContext(ctx).Where("id = ? AND account_id = ?", *operation.PhoneID, operation.AccountID).First(&phone).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrPhoneMissing
		}
		return "", fmt.Errorf("load Kitesim renewal phone: %w", err)
	}
	orderNo := ""
	err = s.withSingleUpstreamClient(ctx, account.Account, proxydomain.ProxyPurposeAuth, func(client *Client) error {
		token, err := s.authenticateOperationClient(ctx, client, account)
		if err != nil {
			return err
		}
		packages, err := client.NumberPackages(ctx, token, phone.CountryCode, "")
		if err != nil {
			return err
		}
		selectedPackage, err := selectOperationPackage(packages, product)
		if err != nil {
			return err
		}
		if priceExceeds(string(selectedPackage.BuyPrice), operation.Amount) {
			return ErrPriceChanged
		}
		orderNo, err = client.CreateRenewalOrder(ctx, token, phoneOrderFromModel(phone), string(selectedPackage.ID))
		if err != nil {
			return err
		}
		refs := operationProviderRefs{
			OrderNos: []string{orderNo}, PreviousExpireTime: phone.ExpireTime,
			PreviousLatestRenewal: phone.LatestRenewal,
		}
		if err := s.recordOperationProgress(ctx, operation.ID, 0, refs); err != nil {
			return err
		}
		confirmErr := client.ConfirmRenewalOrder(ctx, token, orderNo)
		if confirmErr == nil {
			if persistErr := s.recordOperationProgress(ctx, operation.ID, 1, refs); persistErr != nil {
				return errors.Join(ErrPaymentUncertain, persistErr)
			}
			return nil
		}
		return errors.Join(ErrPaymentUncertain, confirmErr)
	})
	return orderNo, err
}

func (s *Service) operationAccountAndProduct(ctx context.Context, operation operationModel) (accountModel, productModel, error) {
	var account accountModel
	if err := s.db.WithContext(ctx).First(&account, operation.AccountID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return accountModel{}, productModel{}, ErrAccountMissing
		}
		return accountModel{}, productModel{}, fmt.Errorf("load Kitesim operation account: %w", err)
	}
	var product productModel
	if err := s.db.WithContext(ctx).
		Where("country_code = ? AND package_id = ?", operation.CountryCode, operation.PackageID).
		First(&product).Error; err != nil {
		return accountModel{}, productModel{}, fmt.Errorf("load Kitesim operation product: %w", err)
	}
	return account, product, nil
}

func selectOperationPackage(packages []NumberPackage, product productModel) (NumberPackage, error) {
	var matched *NumberPackage
	for i := range packages {
		if strings.TrimSpace(string(packages[i].ID)) == strings.TrimSpace(product.PackageID) {
			return packages[i], nil
		}
		if packages[i].DurationType != product.DurationType || packages[i].DurationValue != product.DurationValue {
			continue
		}
		if matched == nil || decimalLess(packages[i].BuyPrice, matched.BuyPrice) {
			matched = &packages[i]
		}
	}
	if matched != nil {
		return *matched, nil
	}
	return NumberPackage{}, errors.New("kitesim: selected package is unavailable for this phone")
}

func confirmPhoneOrderOnce(ctx context.Context, client *Client, token, orderNo string) error {
	err := client.ConfirmPhoneOrder(ctx, token, orderNo)
	if err == nil {
		return nil
	}
	detail, detailErr := client.PhoneOrderDetail(ctx, token, orderNo)
	if detailErr == nil && phoneOrderPaid(*detail) {
		return nil
	}
	return errors.Join(ErrPaymentUncertain, err, detailErr)
}

func phoneOrderPaid(order PhoneOrder) bool {
	return strings.TrimSpace(order.PaymentTime) != "" || strings.TrimSpace(order.ExpireTime) != ""
}

func phoneOrderFromModel(phone phoneModel) PhoneOrder {
	return PhoneOrder{
		ID: stringValue(phone.ProviderOrderID), OrderNo: phone.OrderNo,
		PhoneCode: stringValue(phone.PhoneCode), PhoneNumber: phone.PhoneNumber,
		CountryCode: stringValue(phone.CountryCode), PackageID: stringValue(phone.PackageID),
		Status: PhoneStatus(phone.Status), OrderStatus: phone.OrderStatus,
		DurationType: phone.DurationType, DurationValue: phone.DurationValue,
		CreateTime: phone.CreateTime, PaymentTime: phone.PaymentTime,
		ExpireTime: phone.ExpireTime, LatestRenewalTime: phone.LatestRenewal,
	}
}
