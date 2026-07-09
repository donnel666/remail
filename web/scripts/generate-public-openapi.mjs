import { writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const outputPath = resolve(__dirname, "../public/openapi.json");

const json = (schema) => ({
  content: {
    "application/json": {
      schema,
    },
  },
});

const ref = (name) => ({ $ref: `#/components/schemas/${name}` });
const listOf = (name) => ({ type: "array", items: ref(name) });
const nullable = (type) => ({ type: [type, "null"] });
const stringEnum = (values) => ({ type: "string", enum: values });
const ok = (schema) => ({ description: "请求成功。", ...json(schema) });
const created = (schema) => ({ description: "资源已创建。", ...json(schema) });
const accepted = (schema) => ({ description: "任务已提交。", ...json(schema) });
const noContent = { description: "操作成功，无响应内容。" };
const errorResponses = {
  "400": { $ref: "#/components/responses/BadRequest" },
  "401": { $ref: "#/components/responses/Unauthorized" },
  "403": { $ref: "#/components/responses/Forbidden" },
  "404": { $ref: "#/components/responses/NotFound" },
  "409": { $ref: "#/components/responses/Conflict" },
  "422": { $ref: "#/components/responses/UnprocessableEntity" },
  "429": { $ref: "#/components/responses/TooManyRequests" },
  "500": { $ref: "#/components/responses/InternalError" },
};

const apiKeySecurity = [{ remailApiKey: [] }];

const paginationParams = [
  {
    name: "offset",
    in: "query",
    schema: { type: "integer", minimum: 0, default: 0 },
    description: "分页偏移量。",
  },
  {
    name: "limit",
    in: "query",
    schema: { type: "integer", minimum: 1, maximum: 100, default: 20 },
    description: "每页数量，最大 100。",
  },
];

const idempotencyHeader = {
  name: "Idempotency-Key",
  in: "header",
  required: true,
  schema: { type: "string", minLength: 1, maxLength: 128 },
  description: "幂等键。相同 API Key 下，相同幂等键不会重复创建业务事实。",
};

const schemas = {
  ErrorResponse: {
    type: "object",
    properties: {
      message: { type: "string", example: "Invalid request parameters." },
      requestId: { type: "string", example: "8f7a2f9c-0d4f-4a9c-8e47-9af7a2d6c931" },
      fields: {
        type: "array",
        items: {
          type: "object",
          properties: {
            field: { type: "string" },
            message: { type: "string" },
          },
        },
      },
    },
    required: ["message", "requestId"],
  },
  APIKeyProfileResponse: {
    type: "object",
    properties: {
      apiKey: ref("APIKeyProfile"),
    },
    required: ["apiKey"],
  },
  APIKeyProfile: {
    type: "object",
    properties: {
      id: { type: "integer", example: 18 },
      name: { type: "string", example: "订单服务专用密钥" },
      keyPrefix: { type: "string", example: "rk-550e8400-e2" },
      enabled: { type: "boolean", example: true },
      rateLimitPerMinute: nullable("integer"),
      concurrencyLimit: { type: "integer", example: 5 },
      quotaLimit: nullable("integer"),
      quotaUsed: { type: "integer", example: 372 },
      remainingQuota: nullable("integer"),
      activeRequests: { type: "integer", example: 0 },
      expireAt: nullable("string"),
      lastUsedAt: nullable("string"),
      createdAt: { type: "string", format: "date-time" },
      updatedAt: { type: "string", format: "date-time" },
    },
    required: [
      "id",
      "name",
      "keyPrefix",
      "enabled",
      "concurrencyLimit",
      "quotaUsed",
      "activeRequests",
      "createdAt",
      "updatedAt",
    ],
  },
  ProjectListResponse: {
    type: "object",
    properties: {
      items: listOf("Project"),
      total: { type: "integer", example: 36 },
      offset: { type: "integer", example: 0 },
      limit: { type: "integer", example: 20 },
      facets: ref("ProjectFacets"),
    },
    required: ["items", "total", "offset", "limit"],
  },
  ProjectDetailResponse: {
    type: "object",
    properties: {
      project: ref("Project"),
      products: listOf("ProjectProduct"),
      mailRules: listOf("ProjectMailRule"),
    },
    required: ["project", "products"],
  },
  Project: {
    type: "object",
    properties: {
      id: { type: "integer", example: 1001 },
      name: { type: "string", example: "Microsoft 账号验证码" },
      targetPlatform: { type: "string", example: "Microsoft" },
      logoUrl: { type: "string", example: "/v1/projects/logos/microsoft.png" },
      description: { type: "string", example: "Microsoft 账号注册与登录验证码接收。" },
      status: stringEnum(["reviewing", "listed", "delisted"]),
      accessType: stringEnum(["public", "private"]),
      looseMatch: { type: "boolean", example: false },
      productCount: { type: "integer", example: 2 },
      mailRuleCount: { type: "integer", example: 3 },
      products: listOf("ProjectProductSummary"),
      createdAt: { type: "string", format: "date-time" },
      updatedAt: { type: "string", format: "date-time" },
    },
    required: [
      "id",
      "name",
      "targetPlatform",
      "status",
      "accessType",
      "looseMatch",
      "productCount",
      "mailRuleCount",
      "createdAt",
      "updatedAt",
    ],
  },
  ProjectProductSummary: {
    type: "object",
    properties: {
      id: { type: "integer", example: 2001 },
      type: stringEnum(["microsoft", "domain"]),
      status: stringEnum(["enabled", "disabled"]),
      codeEnabled: { type: "boolean", example: true },
      purchaseEnabled: { type: "boolean", example: true },
      codePrice: { type: "string", example: "0.80" },
      purchasePrice: { type: "string", example: "5.00" },
      codeWindowMinutes: { type: "integer", example: 10 },
      activationWindowMinutes: { type: "integer", example: 30 },
      warrantyMinutes: { type: "integer", example: 1440 },
      totalAvailable: { type: "integer", example: 815 },
      publicAvailable: { type: "integer", example: 300 },
      suffixes: listOf("ProductSuffixInventory"),
    },
  },
  ProductSuffixInventory: {
    type: "object",
    properties: {
      suffix: { type: "string", example: "outlook.com" },
      totalAvailable: { type: "integer", example: 120 },
      publicAvailable: { type: "integer", example: 80 },
    },
    required: ["suffix", "totalAvailable", "publicAvailable"],
  },
  ProjectProduct: {
    allOf: [
      ref("ProjectProductSummary"),
      {
        type: "object",
        properties: {
          projectId: { type: "integer", example: 1001 },
          createdAt: { type: "string", format: "date-time" },
          updatedAt: { type: "string", format: "date-time" },
        },
      },
    ],
  },
  ProjectMailRule: {
    type: "object",
    properties: {
      id: { type: "integer" },
      projectId: { type: "integer" },
      ruleType: stringEnum(["sender", "recipient", "subject", "body"]),
      pattern: { type: "string", example: "\\b(\\d{6,8})\\b" },
      enabled: { type: "boolean" },
      createdAt: { type: "string", format: "date-time" },
      updatedAt: { type: "string", format: "date-time" },
    },
  },
  ProjectFacets: {
    type: "object",
    properties: {
      status: { type: "object", additionalProperties: { type: "integer" } },
      access: { type: "object", additionalProperties: { type: "integer" } },
      match: { type: "object", additionalProperties: { type: "integer" } },
      productType: { type: "object", additionalProperties: { type: "integer" } },
    },
  },
  CreateOrderRequest: {
    type: "object",
    properties: {
      projectId: { type: "integer", example: 1001 },
      productId: { type: "integer", example: 2001 },
      emailSuffix: { type: "string", example: "outlook.com" },
    },
    required: ["projectId", "productId"],
  },
  OrderListResponse: {
    type: "object",
    properties: {
      items: listOf("Order"),
      total: { type: "integer", example: 3 },
      offset: { type: "integer", example: 0 },
      limit: { type: "integer", example: 20 },
    },
    required: ["items", "total", "offset", "limit"],
  },
  Order: {
    type: "object",
    properties: {
      id: { type: "integer", example: 3021 },
      orderNo: { type: "string", example: "R20260709135800983" },
      userId: { type: "integer", example: 1 },
      projectId: { type: "integer", example: 1001 },
      projectProductId: { type: "integer", example: 2001 },
      productType: stringEnum(["microsoft", "domain"]),
      serviceMode: stringEnum(["code", "purchase"]),
      supplyPolicy: stringEnum(["private_first", "public_only"]),
      status: stringEnum(["pending_payment", "paid", "active", "completed", "refunded", "failed", "closed"]),
      payAmount: { type: "string", example: "0.80" },
      refundAmount: { type: "string", example: "0.00" },
      allocationType: { type: "string", example: "microsoft" },
      allocationId: { type: "integer", example: 991 },
      deliveryEmail: { type: "string", example: "mateo.richards@outlook.com" },
      receiveStartedAt: nullable("string"),
      receiveUntil: nullable("string"),
      activatedAt: nullable("string"),
      afterSaleUntil: nullable("string"),
      clientChannel: { type: "string", example: "api_key" },
      apiKeyId: nullable("integer"),
      serviceCleanupStatus: stringEnum(["none", "succeeded", "partial_failure"]),
      serviceToken: { type: "string", example: "st_550e8400-e29b-41d4-a716-446655440000" },
      archivedAt: nullable("string"),
      createdAt: { type: "string", format: "date-time" },
      updatedAt: { type: "string", format: "date-time" },
    },
    required: [
      "id",
      "orderNo",
      "userId",
      "projectId",
      "projectProductId",
      "productType",
      "serviceMode",
      "supplyPolicy",
      "status",
      "payAmount",
      "refundAmount",
      "deliveryEmail",
      "clientChannel",
      "serviceCleanupStatus",
      "createdAt",
      "updatedAt",
    ],
  },
  PickupMailResponse: {
    type: "object",
    properties: {
      items: listOf("MailMessage"),
      fetch: ref("FetchState"),
    },
    required: ["items"],
  },
  MailMessage: {
    type: "object",
    properties: {
      sender: { type: "string", example: "account-security-noreply@accountprotection.microsoft.com" },
      recipient: { type: "string", example: "mateo.richards@outlook.com" },
      receivedAt: { type: "string", format: "date-time" },
      subject: { type: "string", example: "Microsoft account security code" },
      body: { type: "string", example: "Your security code is 829104." },
      verificationCode: { type: "string", example: "829104" },
    },
    required: ["sender", "recipient", "receivedAt", "subject", "body"],
  },
  FetchState: {
    type: "object",
    properties: {
      lastJobId: nullable("integer"),
      lastStatus: { type: "string", example: "succeeded" },
      lastSubmittedAt: nullable("string"),
      lastSuccessAt: nullable("string"),
      lastReceivedAt: nullable("string"),
      nextFetchAllowedAt: nullable("string"),
      lastSafeError: { type: "string" },
    },
  },
  Wallet: {
    type: "object",
    properties: {
      userId: { type: "integer", example: 1 },
      consumerBalance: { type: "string", example: "168.50" },
      supplierAvailable: { type: "string", example: "0.00" },
      supplierFrozen: { type: "string", example: "0.00" },
      historicalSpend: { type: "string", example: "391.20" },
      orderCount: { type: "integer", example: 486 },
      updatedAt: { type: "string", format: "date-time" },
    },
    required: [
      "userId",
      "consumerBalance",
      "supplierAvailable",
      "supplierFrozen",
      "historicalSpend",
      "orderCount",
      "updatedAt",
    ],
  },
  TransactionListResponse: {
    type: "object",
    properties: {
      items: listOf("Transaction"),
      total: { type: "integer" },
      offset: { type: "integer" },
      limit: { type: "integer" },
    },
    required: ["items", "total", "offset", "limit"],
  },
  Transaction: {
    type: "object",
    properties: {
      id: { type: "integer" },
      transactionNo: { type: "string", example: "TX20260709140100188" },
      userId: { type: "integer" },
      transactionType: { type: "string", example: "order_payment" },
      balanceBucket: { type: "string", example: "consumer" },
      direction: stringEnum(["credit", "debit"]),
      amount: { type: "string", example: "0.80" },
      balanceBefore: { type: "string", example: "169.30" },
      balanceAfter: { type: "string", example: "168.50" },
      bizType: { type: "string", example: "order" },
      bizId: { type: "string", example: "R20260709135800983" },
      createdAt: { type: "string", format: "date-time" },
    },
  },
  RechargeListResponse: {
    type: "object",
    properties: {
      items: listOf("Recharge"),
      total: { type: "integer" },
      offset: { type: "integer" },
      limit: { type: "integer" },
    },
    required: ["items", "total", "offset", "limit"],
  },
  Recharge: {
    type: "object",
    properties: {
      id: { type: "integer" },
      rechargeNo: { type: "string", example: "RC20260709141500920" },
      userId: { type: "integer" },
      paymentMethod: { type: "string", example: "card" },
      rechargeQuota: { type: "string", example: "100.00" },
      paymentAmount: { type: "string", example: "100.00" },
      status: { type: "string", example: "succeeded" },
      createdAt: { type: "string", format: "date-time" },
      updatedAt: { type: "string", format: "date-time" },
    },
  },
  RedeemCardRequest: {
    type: "object",
    properties: {
      cardKey: { type: "string", example: "RKCD-2026-8P2M-9Q4X" },
    },
    required: ["cardKey"],
  },
  RedeemCardResponse: {
    type: "object",
    properties: {
      wallet: ref("Wallet"),
      transaction: ref("Transaction"),
      card: ref("CardKey"),
    },
    required: ["wallet", "transaction", "card"],
  },
  CardKey: {
    type: "object",
    properties: {
      cardKey: { type: "string" },
      amount: { type: "string" },
      status: { type: "string" },
      maxRedemptions: { type: "integer" },
      redeemedCount: { type: "integer" },
      expireAt: nullable("string"),
      createdByUserId: nullable("integer"),
      createdAt: { type: "string", format: "date-time" },
      updatedAt: { type: "string", format: "date-time" },
    },
  },
  ResourceListResponse: {
    type: "object",
    properties: {
      items: listOf("Resource"),
      total: { type: "integer" },
      offset: { type: "integer" },
      limit: { type: "integer" },
    },
    required: ["items", "total", "offset", "limit"],
  },
  Resource: {
    type: "object",
    properties: {
      id: { type: "integer", example: 9081 },
      type: stringEnum(["microsoft", "domain"]),
      ownerId: { type: "integer" },
      status: { type: "string", example: "valid" },
      forSale: nullable("boolean"),
      longLived: nullable("boolean"),
      graphAvailable: nullable("boolean"),
      lastSafeError: { type: "string" },
      email: { type: "string", example: "mateo.richards@outlook.com" },
      domain: { type: "string", example: "example-mail.com" },
      domainTld: { type: "string", example: "com" },
      mailServerId: { type: "integer" },
      purpose: { type: "string", example: "not_sale" },
      mailboxCount: { type: "integer", example: 10000 },
      createdAt: { type: "string", format: "date-time" },
    },
  },
  ResourceDetail: {
    oneOf: [ref("MicrosoftResourceDetail"), ref("DomainResourceDetail")],
  },
  MicrosoftResourceDetail: {
    type: "object",
    properties: {
      id: { type: "integer" },
      emailAddress: { type: "string" },
      forSale: { type: "boolean" },
      longLived: { type: "boolean" },
      graphAvailable: { type: "boolean" },
      status: { type: "string" },
      qualityScore: { type: "integer" },
      lastSafeError: { type: "string" },
      lastAllocatedAt: nullable("string"),
      createdAt: { type: "string", format: "date-time" },
    },
  },
  DomainResourceDetail: {
    type: "object",
    properties: {
      id: { type: "integer" },
      domain: { type: "string" },
      mailServerId: { type: "integer" },
      purpose: { type: "string" },
      status: { type: "string" },
      lastSafeError: { type: "string" },
      lastAllocatedAt: nullable("string"),
      createdAt: { type: "string", format: "date-time" },
    },
  },
  ImportAcceptedResponse: {
    type: "object",
    properties: {
      importId: { type: "integer" },
      imported: { type: "integer" },
    },
    required: ["importId", "imported"],
  },
  ImportStatusResponse: {
    type: "object",
    properties: {
      importId: { type: "integer" },
      status: { type: "string", example: "processing" },
      imported: { type: "integer" },
      lastSafeError: { type: "string" },
      createdAt: { type: "string", format: "date-time" },
      updatedAt: { type: "string", format: "date-time" },
    },
    required: ["importId", "status", "imported", "createdAt", "updatedAt"],
  },
  ResourceValidationsResponse: {
    type: "object",
    properties: {
      requested: { type: "integer" },
      queued: { type: "integer" },
    },
    required: ["requested", "queued"],
  },
  ResourceValidation: {
    type: "object",
    properties: {
      validationId: { type: "integer" },
      resourceId: { type: "integer" },
      resourceType: { type: "string" },
      status: { type: "string" },
      lastSafeError: { type: "string" },
      createdAt: { type: "string", format: "date-time" },
      updatedAt: { type: "string", format: "date-time" },
    },
  },
  ValidateResourcesRequest: {
    type: "object",
    properties: {
      selection: ref("ResourceBulkSelection"),
    },
    required: ["selection"],
  },
  ResourceBulkSelection: {
    type: "object",
    properties: {
      mode: stringEnum(["ids", "filter"]),
      resourceIds: { type: "array", items: { type: "integer" } },
      filter: {
        type: "object",
        properties: {
          resourceType: stringEnum(["all", "microsoft", "domain"]),
          search: { type: "string" },
          status: { type: "string" },
          forSale: nullable("boolean"),
          longLived: nullable("boolean"),
          graphAvailable: nullable("boolean"),
        },
      },
    },
    required: ["mode"],
  },
  MailServerListResponse: {
    type: "object",
    properties: {
      items: listOf("MailServer"),
      total: { type: "integer" },
      offset: { type: "integer" },
      limit: { type: "integer" },
    },
    required: ["items", "total", "offset", "limit"],
  },
  MailServer: {
    type: "object",
    properties: {
      id: { type: "integer" },
      name: { type: "string" },
      serverAddress: { type: "string" },
      status: { type: "string" },
      createdAt: { type: "string", format: "date-time" },
    },
  },
  CreateMailServerRequest: {
    type: "object",
    properties: {
      name: { type: "string", example: "mx-01" },
      serverAddress: { type: "string", example: "mx01.example-mail.com" },
      mxRecord: { type: "string" },
      spfRecord: { type: "string" },
      dkimRecord: { type: "string" },
      dmarcRecord: { type: "string" },
      ptrRecord: { type: "string" },
    },
    required: ["serverAddress"],
  },
  CreateDomainRequest: {
    type: "object",
    properties: {
      domain: { type: "string", example: "example-mail.com" },
      mailServerId: { type: "integer", example: 12 },
      purpose: { type: "string", example: "not_sale" },
    },
    required: ["domain"],
  },
  MailboxListResponse: {
    type: "object",
    properties: {
      items: listOf("Mailbox"),
      total: { type: "integer" },
      offset: { type: "integer" },
      limit: { type: "integer" },
    },
    required: ["items", "total", "offset", "limit"],
  },
  Mailbox: {
    type: "object",
    properties: {
      id: { type: "integer" },
      email: { type: "string" },
      status: { type: "string" },
      lastAllocatedAt: nullable("string"),
      createdAt: { type: "string", format: "date-time" },
    },
  },
};

const spec = {
  openapi: "3.1.0",
  info: {
    title: "ReMail 开放 API",
    version: "1.0.0",
  },
  tags: [
    { name: "Core", description: "高频集成接口，覆盖 API Key 状态、项目查询、统一下单、订单查询和邮件取件。" },
    { name: "Resources", description: "自有微软邮箱、域名邮箱、邮件服务器和资源检测接口。" },
    { name: "Wallet", description: "钱包余额、账单、充值记录和兑换码充值接口。" },
    { name: "Tickets", description: "售后工单接口，后端能力落地后开放。" },
  ],
  components: {
    securitySchemes: {
      remailApiKey: {
        type: "http",
        scheme: "bearer",
        bearerFormat: "API Key",
        description: "填入 rk- 开头的 API Key 即可，Swagger UI 会自动加上 Bearer 前缀。",
      },
    },
    responses: {
      BadRequest: { description: "请求参数错误。", ...json(ref("ErrorResponse")) },
      Unauthorized: { description: "缺少或无效认证。", ...json(ref("ErrorResponse")) },
      Forbidden: { description: "无权访问该资源。", ...json(ref("ErrorResponse")) },
      NotFound: { description: "资源不存在。", ...json(ref("ErrorResponse")) },
      Conflict: { description: "请求与已有业务事实冲突。", ...json(ref("ErrorResponse")) },
      UnprocessableEntity: { description: "业务校验未通过。", ...json(ref("ErrorResponse")) },
      TooManyRequests: { description: "请求超过 API Key 限制。", ...json(ref("ErrorResponse")) },
      InternalError: { description: "服务端异常。", ...json(ref("ErrorResponse")) },
    },
    schemas,
  },
  paths: {
    "/v1/open/apikey/profile": {
      get: {
        tags: ["Core"],
        operationId: "getApiKeyProfile",
        summary: "查询当前 API Key",
        description: "返回当前 API Key 的额度、RPM、并发、过期时间和使用状态。",
        security: apiKeySecurity,
        responses: {
          "200": ok(ref("APIKeyProfileResponse")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/projects": {
      get: {
        tags: ["Core"],
        operationId: "listProjects",
        summary: "查询项目",
        description: "查询当前用户可见的公共项目、已授权私有项目和自己的申请记录。",
        security: apiKeySecurity,
        parameters: [
          ...paginationParams,
          { name: "scope", in: "query", schema: stringEnum(["visible", "mine"]) },
          { name: "status", in: "query", schema: { type: "string" } },
          { name: "accessType", in: "query", schema: stringEnum(["public", "private"]) },
          { name: "productType", in: "query", schema: stringEnum(["microsoft", "domain"]) },
          { name: "search", in: "query", schema: { type: "string" } },
        ],
        responses: {
          "200": ok(ref("ProjectListResponse")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/projects/{projectId}": {
      get: {
        tags: ["Core"],
        operationId: "getProject",
        summary: "查询项目详情",
        security: apiKeySecurity,
        parameters: [{ name: "projectId", in: "path", required: true, schema: { type: "integer" } }],
        responses: {
          "200": ok(ref("ProjectDetailResponse")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/orders": {
      post: {
        tags: ["Core"],
        operationId: "createOrder",
        summary: "统一下单",
        description: "通过查询参数指定长效购买或短效接码，以及库存策略。",
        security: apiKeySecurity,
        parameters: [
          idempotencyHeader,
          { name: "serviceMode", in: "query", schema: stringEnum(["purchase", "code"]), description: "purchase 为长效购买，code 为短效接码。" },
          { name: "supply", in: "query", schema: stringEnum(["private_first", "public_only"]), description: "默认 private_first。" },
        ],
        requestBody: json(ref("CreateOrderRequest")),
        responses: {
          "200": ok(ref("Order")),
          "201": created(ref("Order")),
          ...errorResponses,
        },
      },
      get: {
        tags: ["Core"],
        operationId: "listOrders",
        summary: "查询订单",
        security: apiKeySecurity,
        parameters: [
          ...paginationParams,
          { name: "status", in: "query", schema: { type: "string" } },
          { name: "serviceMode", in: "query", schema: stringEnum(["purchase", "code"]) },
          { name: "search", in: "query", schema: { type: "string" } },
        ],
        responses: {
          "200": ok(ref("OrderListResponse")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/orders/{orderNo}": {
      get: {
        tags: ["Core"],
        operationId: "getOrder",
        summary: "查询订单详情",
        security: apiKeySecurity,
        parameters: [{ name: "orderNo", in: "path", required: true, schema: { type: "string" } }],
        responses: {
          "200": ok(ref("Order")),
          ...errorResponses,
        },
      },
    },
    "/v1/pickup": {
      get: {
        tags: ["Core"],
        operationId: "pickupMessages",
        summary: "取件读取邮件",
        description: "取件接口不使用 API Key。持有交付邮箱和服务凭证即可访问对应订单的邮件结果。",
        security: [],
        parameters: [
          { name: "email", in: "query", required: true, schema: { type: "string", format: "email" } },
          { name: "token", in: "query", required: true, schema: { type: "string" } },
        ],
        responses: {
          "200": ok(ref("PickupMailResponse")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/wallet": {
      get: {
        tags: ["Wallet"],
        operationId: "getWallet",
        summary: "查询钱包",
        security: apiKeySecurity,
        responses: {
          "200": ok(ref("Wallet")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/wallet/transactions": {
      get: {
        tags: ["Wallet"],
        operationId: "listWalletTransactions",
        summary: "查询钱包流水",
        security: apiKeySecurity,
        parameters: [...paginationParams, { name: "search", in: "query", schema: { type: "string" } }],
        responses: {
          "200": ok(ref("TransactionListResponse")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/recharges": {
      get: {
        tags: ["Wallet"],
        operationId: "listRecharges",
        summary: "查询充值记录",
        security: apiKeySecurity,
        parameters: [
          ...paginationParams,
          { name: "status", in: "query", schema: { type: "string" } },
          { name: "search", in: "query", schema: { type: "string" } },
        ],
        responses: {
          "200": ok(ref("RechargeListResponse")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/cards/redeem": {
      post: {
        tags: ["Wallet"],
        operationId: "redeemCard",
        summary: "兑换码充值",
        security: apiKeySecurity,
        parameters: [idempotencyHeader],
        requestBody: json(ref("RedeemCardRequest")),
        responses: {
          "200": ok(ref("RedeemCardResponse")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/resources": {
      get: {
        tags: ["Resources"],
        operationId: "listResources",
        summary: "查询自有资源",
        security: apiKeySecurity,
        parameters: [
          ...paginationParams,
          { name: "scope", in: "query", schema: stringEnum(["owned"]) },
          { name: "type", in: "query", schema: stringEnum(["all", "microsoft", "domain"]) },
        ],
        responses: {
          "200": ok(ref("ResourceListResponse")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/resources/{resourceId}": {
      get: {
        tags: ["Resources"],
        operationId: "getResource",
        summary: "查询资源详情",
        security: apiKeySecurity,
        parameters: [{ name: "resourceId", in: "path", required: true, schema: { type: "integer" } }],
        responses: {
          "200": ok(ref("ResourceDetail")),
          ...errorResponses,
        },
      },
      delete: {
        tags: ["Resources"],
        operationId: "deleteResource",
        summary: "删除自有资源",
        security: apiKeySecurity,
        parameters: [{ name: "resourceId", in: "path", required: true, schema: { type: "integer" } }],
        responses: {
          "204": noContent,
          ...errorResponses,
        },
      },
    },
    "/v1/open/resources/{resourceId}/validate": {
      post: {
        tags: ["Resources"],
        operationId: "validateResource",
        summary: "提交单个资源检测",
        security: apiKeySecurity,
        parameters: [{ name: "resourceId", in: "path", required: true, schema: { type: "integer" } }],
        responses: {
          "202": accepted(ref("ResourceValidation")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/resources/imports": {
      post: {
        tags: ["Resources"],
        operationId: "importMicrosoftResources",
        summary: "导入微软邮箱 TXT",
        security: apiKeySecurity,
        requestBody: {
          content: {
            "multipart/form-data": {
              schema: {
                type: "object",
                properties: {
                  file: { type: "string", format: "binary" },
                  longLived: { type: "boolean", example: true },
                  errorStrategy: stringEnum(["skip", "abort"]),
                },
                required: ["file", "longLived", "errorStrategy"],
              },
            },
          },
        },
        responses: {
          "202": accepted(ref("ImportAcceptedResponse")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/resources/imports/{importId}": {
      get: {
        tags: ["Resources"],
        operationId: "getResourceImport",
        summary: "查询导入任务",
        security: apiKeySecurity,
        parameters: [{ name: "importId", in: "path", required: true, schema: { type: "integer" } }],
        responses: {
          "200": ok(ref("ImportStatusResponse")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/resources/validations": {
      post: {
        tags: ["Resources"],
        operationId: "validateResources",
        summary: "批量提交资源检测",
        security: apiKeySecurity,
        requestBody: json(ref("ValidateResourcesRequest")),
        responses: {
          "202": accepted(ref("ResourceValidationsResponse")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/resources/validations/{validationId}": {
      get: {
        tags: ["Resources"],
        operationId: "getResourceValidation",
        summary: "查询资源检测任务",
        security: apiKeySecurity,
        parameters: [{ name: "validationId", in: "path", required: true, schema: { type: "integer" } }],
        responses: {
          "200": ok(ref("ResourceValidation")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/servers": {
      get: {
        tags: ["Resources"],
        operationId: "listMailServers",
        summary: "查询自有邮件服务器",
        security: apiKeySecurity,
        parameters: [...paginationParams, { name: "scope", in: "query", schema: stringEnum(["owned"]) }],
        responses: {
          "200": ok(ref("MailServerListResponse")),
          ...errorResponses,
        },
      },
      post: {
        tags: ["Resources"],
        operationId: "createMailServer",
        summary: "创建邮件服务器",
        security: apiKeySecurity,
        requestBody: json(ref("CreateMailServerRequest")),
        responses: {
          "201": created(ref("MailServer")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/domains": {
      post: {
        tags: ["Resources"],
        operationId: "createDomain",
        summary: "创建域名邮箱资源",
        security: apiKeySecurity,
        requestBody: json(ref("CreateDomainRequest")),
        responses: {
          "201": created(ref("DomainResourceDetail")),
          ...errorResponses,
        },
      },
    },
    "/v1/open/domains/{domainId}/mailboxes": {
      get: {
        tags: ["Resources"],
        operationId: "listDomainMailboxes",
        summary: "查询域名邮箱生成记录",
        security: apiKeySecurity,
        parameters: [
          { name: "domainId", in: "path", required: true, schema: { type: "integer" } },
          ...paginationParams,
        ],
        responses: {
          "200": ok(ref("MailboxListResponse")),
          ...errorResponses,
        },
      },
    },
  },
};

writeFileSync(outputPath, `${JSON.stringify(spec, null, 2)}\n`);
console.log(`Generated ${outputPath}`);
