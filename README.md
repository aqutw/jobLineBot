# jobLineBot

104 職缺分析與模擬面試 LINE Bot，Go API + OpenRouter `openrouter/free` + Render Free。

使用者傳送 `8tv2k` 或 `https://www.104.com.tw/job/8tv2k/`，取得職責／技能分析、6 題附考察重點與參考答案的模擬面試題，以及可反問面試官的問題。模擬題並非公司實際考題。

## 流程

```text
LINE → Go 驗證簽章 → 有界記憶體佇列 → Go 擷取104 HTML
                                      ↓
                    HTML 有本文：解析 JobPosting / main
                    HTML 是 SPA 空殼：Go 擷取104職缺內容API
                                      ↓
                Go HTML parser 清理：移除 script / style / 導覽等
                                      ↓
          已清理職缺文件 → OpenRouter LLM → 分析、模擬題與答案 → LINE
```

模型未配置 tools、web search 或 plugins，不會自行開網址。資料來源、抓取時間、清理本文及實際回傳模型都可由受保護 HTTP API 檢查。104 抓取以 Chrome 131 TLS 指紋及 Referer 實作，參考使用者提供的 `_job104_common.sh` / `get_job_detail.sh`。**不是將網址直接交給 LLM**。

## 本機啟動

Go 1.22+（CI／Render 使用 1.25.0）。

```bash
cp .env.example .env
# 填入自己的設定；.env 已被 gitignore
set -a
. ./.env
set +a
go test -race ./...
go vet ./...
go run ./cmd/bot
```

macOS 若使用舊 Go 遇到 `missing LC_UUID`，可用 `go test -race -ldflags=-linkmode=external ./...`。

| 環境變數 | 說明 |
|---|---|
| OPENROUTER_API_KEY | OpenRouter key；勿加 Bearer 前綴 |
| OPENROUTER_MODEL | 預設 `openrouter/free` |
| API_TOKEN | 受保護測試 API 的 Bearer token；留空時 API 回404 |
| LINE_CHANNEL_SECRET | **本 bot** Messaging API channel secret |
| LINE_CHANNEL_ACCESS_TOKEN | **本 bot** access token |
| PORT | 預設10000；Render 自動提供 |
| JOB104_PROXY_URL | 選用；104 擷取專用的 HTTP/HTTPS/SOCKS5 代理，可含驗證帳密；僅存環境變數 |

## HTTP API

- `GET /`：使用說明。
- `GET /healthz`：程序及金鑰是否設定；不會呼叫模型或104。`status=ok` 不代表 LINE 已完成設定。
- `POST /api/analyze`：需 `Authorization: Bearer $API_TOKEN`，接受 `job_id`、`url` 或 `text` 其中一個，以及選用 `prompt`（準備方向，各欄位最多2000字）。
- `POST /line/webhook`：驗證 HMAC-SHA256 LINE 簽章，不接受未簽章要求。

```bash
curl http://localhost:10000/api/analyze \
  -H "Authorization: Bearer $API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"job_id":"8tv2k","prompt":"著重系統設計與生成式AI"}'
```

回傳 `text`、`model`、`job`。`job.cleaned_text` 是實際送給模型的整理內容，`job.extraction` 表示 `html` 或 `html+104_content_api`。錯誤回400（輸入）、401（驗證）、429（忙碌）、502（104抓取）、503（模型）；不會用虛構資料補足抓取失敗。

## Render / GitHub

私有 repo 使用獨立專案，不含其他 bot 的資料與密鑰。`render.yaml` 提供 Free Web Service：Singapore、Go、`go build -tags netgo -trimpath -ldflags="-s -w" -o bin/bot ./cmd/bot`、`./bin/bot`、健康檢查 `/healthz`。在 Render 設定環境變數；不要把 `.env` 上傳 GitHub。

GitHub Actions 執行 race tests、vet 與 Linux build。可選擇執行公開職缺連線測試：

```bash
LIVE_JOB104=1 go test -v -run TestLiveJob104 ./internal/job104
```

## LINE 連線

1. 建立 jobLineBot LINE Official Account，啟用 Messaging API。
2. 將此 channel 的 secret/token 放入 Render，勿使用其他 bot 的憑證。
3. Webhook URL 設為 `https://<your-service>.onrender.com/line/webhook`。
4. Verify，啟用 Use webhook 與 webhook redelivery，關閉預設自動回覆。
5. 加好友後傳 `8tv2k`，檢查分析與面試題是否送達。

簽章驗證成功後快速 HTTP ACK，worker 先用 Reply API 回覆處理進度，再於分析完成後以 Push API 傳送結果，避免免費模型耗時超過 reply token 時效。完成推播會使用 LINE 官方帳號的訊息額度。長文按 LINE 5000 UTF-16 units 上限切為最多5則訊息。

## 限制

- 僅擷取固定104主機與合法 job_id，不追蹤重新導向；防止 URL 形成任意伺服器端請求。
- 104 每秒至多一個新請求，成功文件快取10分鐘／上限128份。回應最多2 MiB，清理本文最多30000字，截斷時回傳 `truncated=true`。
- LINE 與 HTTP 分析有150秒總預算，模型請求上限120秒。免費模型忙碌或冷啟動仍可能逾時。
- Free model 可用性、速率與品質取決於 OpenRouter；不自動切換付費模型。
- LINE 佇列上限16，單 worker；去重20分鐘／2048事件。佇列／快取／去重皆在記憶體，重啟會失去資料。正式可靠投遞需要持久化工作佇列。
- 不記錄使用者訊息、LINE ID、reply token 或 API key；只記錄匿名服務錯誤。
- Render Free 有休眠／冷啟動，104亦可能限制資料中心 IP；實際部署後須測試公開職缺抓取。
- 無個人履歷、對話記憶或履歷符合度評分。模型推論與模擬答案應由使用者依經驗確認。

實作參考：[OpenRouter API](https://openrouter.ai/docs/api-reference/overview)、[LINE Webhook](https://developers.line.biz/en/docs/messaging-api/receiving-messages/)、[Render Blueprint](https://render.com/docs/blueprint-spec)。

### Render 擷取104回403時

實測發現104對目前Render服務的HTML及職缺內容API請求皆回HTTP403；本機相同job_id可取得。健康檢查的憑證設定旗標不代表104連線已成功。可在Render設定自有、能正常存取104的代理 `JOB104_PROXY_URL`，例如 `http://user:password@proxy-host:port`。此代理只用於Go的104擷取，OpenRouter及LINE不經代理；TLS驗證仍開啟。不要將代理帳密提交Git，也不要使用未知的公開代理。未取得有效職缺時，服務會如實回錯誤，不會交給模型自行開網址或捏造資料。

擷取器現在保留職缺頁與內容 API 之間的第一方 cookie，並區分 HTML 導覽與同站 XHR 的標頭。cookie 僅存於程序記憶體，不使用使用者瀏覽器的登入資料。這能修正需要維持會話的請求；不保證能通過要求 JavaScript 或人工驗證的網站防護。

以相同 Go 擷取器直接檢測，不會呼叫 LLM 或傳送 LINE 訊息：

```bash
go run ./cmd/probe -job 8tv2k
```

在本機與部署環境分別執行，成功時回傳標題、擷取方式與字數；失敗時回傳 `diagnostic`。同一份診斷也會出現在受 Bearer token 保護的 `/api/analyze` 錯誤回應中，LINE 聊天仍只顯示一般錯誤訊息。

| diagnostic.reason | 意義 |
|---|---|
| `challenge` | 回應帶有官方 `cf-mitigated: challenge` 標頭；需處理網站驗證，不能僅憑此判定封鎖 IP |
| `forbidden` | HTTP 403，尚無足夠證據判定其防護原因 |
| `rate_limited` | HTTP 429，不會自動反覆重試 |
| `not_found` | HTTP 404／410，職缺可能已移除 |
| `redirect` | 收到重新導向；擷取器仍不自動跟隨 |
| `invalid_content` | HTTP 200，但職缺欄位或本文不足，不會交給 LLM 分析 |
| `timeout`／`transport_error` | 連線問題；請檢查網路／代理 |

診斷僅含階段、HTTP 狀態、分類及格式驗證後的 CF-Ray，不包含 HTML、cookie、proxy 密碼或 API key。


## Chrome擴充功能快照分析

`POST /api/extension/analyze` 為免登入的專用快照API，不需管理token。接受 `snapshot: {id,url,title,company,content,captured_at}` 與 `preferences`；網址必須為canonical104職缺網址、id一致，本文80～40000字、標題／公司最多300字、準備方向2000字，整體請求最多256KB。Go驗證並清理後直接呼叫既有模型，不重新抓104、不接受任意system prompt或模型參數；回傳 `{text,created_at}`，不回傳供應商與憑證。

分析金鑰仍只存在Render環境變數，模型維持 `openrouter/free`。端點不永久保存或記錄快照。匿名使用全服務共用速率限制（burst6，每分鐘補1）及既有2個併發名額。OPTIONS支援合法Chrome擴充功能Origin；CORS不是身分驗證，公開端點的資源限制由伺服器實施。既有 `/api/analyze` 的Bearer驗證與LINE webhook不變。
