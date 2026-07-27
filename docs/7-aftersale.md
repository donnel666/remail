# BC-AFTERSALE 售后仲裁上下文

## 修订记录

| 日期 | 版本 | 修订人 | 说明 |
|------|------|--------|------|
| 2026-06-29 | V1.0 | Codex | 形成 Go 版从 0 DDD 设计基线，作为一次 V1.0 变更。 |
| 2026-07-26 | V1.1 | Codex | 普通用户的供应商权限申请复用 general 工单；AFTERSALE 只记录申请会话，管理员人工修改 IAM 角色，不新增供应商审批状态机。 |
| 2026-07-26 | V1.2 | Codex | 供应商支付宝提现和转入用户钱包统一复用 general 工单；支付宝申请必须携带收款码附件，资金操作仍由管理员人工完成。 |
| 2026-07-27 | V1.3 | Codex | general 工单仅承载支付宝提现并继续强制附收款码；转入用户钱包改由 BC-BILLING 用户侧接口直接完成，不再进入售后工单。 |

> 支撑域。售后只拥有工单状态机，不拥有订单和退款执行。

---

## 1. 定位

| 拥有 | 不拥有 |
|------|--------|
| 工单、消息、附件、流转日志、供应商 SLA、自动检测、平台裁决 | 订单归属、钱包退款、邮箱匹配规则 |

订单工单只保存 `orderNo`，归属、售后窗口、供应商处理人通过 BC-TRADE Port 查询。

---

## 2. 实体

| 实体 | 字段/状态 |
|------|-----------|
| `Ticket` | `ticketNo`、`ticketType(order/general)`、`orderNo`、`problemCode`、`appeal`、`status`、`assigneeUserId`、`supplierDeadlineAt`、`resolution` |
| `TicketMessage` | `senderType(user/supplier/admin/system)`、`content` |
| `Attachment` | 文件名、MIME、大小、MinIO objectKey、上传人 |
| `FlowLog` | `action`、`fromStatus/toStatus`、`operator`、安全上下文 |

---

## 3. 状态机

```mermaid
stateDiagram-v2
    [*] --> checking: 可自动检测订单工单
    [*] --> supplier: 有供应商处理人
    [*] --> platform: 普通/无供应商
    checking --> resolved: 检测失败并自动退款
    checking --> rejected: 检测正常
    checking --> supplier: 检测不确定且有供应商
    checking --> platform: 检测不确定且无供应商
    supplier --> resolved: 供应商解决
    supplier --> platform: 超时/用户升级/供应商转交
    platform --> processing: 管理员认领
    processing --> resolved: 管理员解决
    processing --> rejected: 管理员驳回
    resolved --> platform: 用户重开
    resolved --> closed: 用户确认/自动关闭
    supplier --> cancelled: 用户取消
    platform --> cancelled: 用户取消
    processing --> cancelled: 用户取消
```

`closed/cancelled/rejected` 是终态，`resolved` 非终态，可重开或确认关闭。

---

## 4. 不变式

| 编号 | 规则 |
|------|------|
| INV-AS1 | `ticketType=order` 必须经 Trade 校验订单归属和售后窗口。 |
| INV-AS2 | 可自动检测的问题码必须来自系统内置清单。 |
| INV-AS3 | 自动检测只能产出失败/正常/不确定；退款必须调用 TradeRefundPort。 |
| INV-AS4 | 有供应商处理人时先进入供应商处理，1 小时未解决升级平台。 |
| INV-AS5 | 每次流转写 `FlowLog`。 |
| INV-AS6 | 附件存储失败写 SystemLog，不暴露 objectKey。 |
| INV-AS7 | 通知是旁路事实，失败不回滚工单主流程。 |
| INV-AS8 | 支付宝提现工单只记录请求，不预冻结或修改钱包；管理员处理时必须重新核对供应商可用余额。 |

---

## 5. Port

| Port | 方向 | 职责 |
|------|------|------|
| `OrderPort` | 出站到 BC-TRADE | 查订单归属、售后窗口和供应商处理人。 |
| `RefundPort` | 出站到 BC-TRADE | 自动检测失败后发起退款。 |
| `HealthPort` | 出站到 BC-MAILMATCH | 检测购买邮箱是否能正常收件。 |
| `NotifyPort` | 出站到 BC-GOVERNANCE | 发送站内通知。 |

---

## 6. API 设计

统一业务 API：

| 方法 | URI | 说明 |
|------|-----|------|
| `GET` | `/v1/tickets` | 工单列表；支持 `scope=mine/assigned/all`。 |
| `POST` | `/v1/tickets` | 创建工单。 |
| `GET` | `/v1/tickets/{ticketNo}` | 工单详情。 |
| `POST` | `/v1/tickets/{ticketNo}/messages` | 回复。 |
| `POST` | `/v1/tickets/{ticketNo}/attachments` | 上传附件。 |
| `GET` | `/v1/tickets/{ticketNo}/attachments/{attachmentId}` | 读取附件。 |
| `POST` | `/v1/tickets/{ticketNo}/close` | 用户确认关闭。 |
| `POST` | `/v1/tickets/{ticketNo}/cancel` | 用户取消。 |
| `POST` | `/v1/tickets/{ticketNo}/reopen` | 用户重开。 |
| `POST` | `/v1/tickets/{ticketNo}/escalate` | 用户或供应商升级平台。 |
| `POST` | `/v1/tickets/{ticketNo}/resolve` | 供应商解决已指派工单。 |

供应商权限申请复用 `POST /v1/tickets`：`ticketType=general`、标题固定为“供应商申请”、首条消息为用户填写内容。AFTERSALE 不修改用户角色；管理员看到工单后通过 IAM 用户管理人工将申请人角色改为 `supplier`。

供应商提现到支付宝复用 `POST /v1/tickets`：`ticketType=general`、标题固定为“供应商提现申请”，首条消息按固定格式记录提现金额、支付宝去向和用户备注，并必须带一张支付宝收款码图片附件。AFTERSALE 不执行资金操作，管理员核对申请人实时余额后通过 Billing 后台钱包能力人工处理，并在工单中回复和关闭。转入用户钱包不创建工单，由 BC-BILLING `POST /v1/wallet/supplier-transfers` 直接完成。

后台/供应商特权动作：

| 方法 | URI | 说明 |
|------|-----|------|
| `POST` | `/v1/admin/tickets/{ticketNo}/claim` | 管理员认领。 |
| `POST` | `/v1/admin/tickets/{ticketNo}/assign` | 改派。 |
| `POST` | `/v1/admin/tickets/{ticketNo}/resolve` | 管理员解决。 |
| `POST` | `/v1/admin/tickets/{ticketNo}/reject` | 管理员驳回，必须有业务原因。 |

---

## 7. ADR

| ADR | 决策 | 理由 |
|-----|------|------|
| ADR-AS-1 | 售后不冗余订单快照 | 避免工单数据过期，订单事实实时查。 |
| ADR-AS-2 | 自动检测不另建聚合 | `problemCode + FlowLog` 足够表达检测过程。 |
| ADR-AS-3 | 退款只经 Trade | 保证订单、钱包、分配和凭证同步。 |
| ADR-AS-4 | 通知为旁路 | 通知失败不能影响售后状态机。 |
