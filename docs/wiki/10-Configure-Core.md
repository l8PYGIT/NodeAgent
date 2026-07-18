<div dir="rtl">

[🇬🇧 English](10-Configure-Core-EN) · **🇮🇷 فارسی**

# تنظیم هسته‌ی نود (Xray) توسط فروشنده

این صفحه دقیق توضیح می‌دهد که تو به‌عنوان **اپراتور/فروشنده** چطور کانفیگ هسته‌ی Xray نود را تنظیم می‌کنی. این کانفیگ **برای همه‌ی مشتری‌ها مشترک** است؛ کاربرهای هر مشتری به‌صورت خودکار داخل همین inboundها تزریق می‌شوند.

## مفهوم‌های کلیدی (حتماً بخوان)
- نود یک **کانفیگ ثابت** دارد: فایل `/var/lib/pg-node-agent/fixed-config.json` روی سرور.
- آرایه‌ی `clients` داخل inbound را **خالی** بگذار. کاربرها را سیستم به ازای هر مشتری اضافه/حذف می‌کند؛ تو فقط «شکلِ» inbound (پورت/پروتکل/TLS/...) را تعریف می‌کنی.
- کاربرهای مشتری به‌صورت خودکار روی proxy inboundهای کانفیگ نشانده می‌شوند، پس هر تگی کار می‌کند — نیازی نیست با `PG_AGENT_FORCE_INBOUNDS` بخورد (خالی بگذارش). این env را فقط برای محدود کردن کاربرها به زیرمجموعه‌ای از inboundها تنظیم کن.
- چون نود حجمی است، کانفیگی که پنل PasarGuard مشتری می‌فرستد **نادیده** گرفته می‌شود؛ فقط همین کانفیگِ تو اجرا می‌شود.
- **مشتری باید همین inbound را در پنل خودش بسازد** تا لینک کاربرهایش کار کند (پورت/پروتکل/SNI/Reality و... باید یکی باشد). جزئیات در انتهای صفحه.

## سه راه برای تنظیم کانفیگ

### راه ۱ — رابط وب پنل (ساده‌ترین)
`http://PANEL_IP:8080/` → بخش **نودها** → روی نود دکمه‌ی **«جزئیات»** → از آنجا می‌توانی:
- **کانفیگ Xray** را ببینی/ویرایش/اعمال کنی (قالب آماده هم دارد).
- **شروع / ری‌استارت / توقف هسته** را بزنی (مثل نود عادی PasarGuard).
- **تغییر نسخه Xray** (نسخه را وارد کن یا `latest`؛ نود دانلود و هسته را ری‌استارت می‌کند).
- **آپدیت نود** (باینری نود را به آخرین نسخه می‌برد و سرویس را ری‌استارت می‌کند — مثل اسکریپت).
- master key / core key / گواهی / آدرس‌ها را با دکمه‌ی کپی برداری، یا با **«ویرایش نود»** عوضشان کنی.

### راه ۲ — API پنل
```bash
# گرفتن کانفیگ زنده‌ی فعلی
curl http://PANEL_IP:8080/api/v1/nodes/<NODE_ID>/config -H "Authorization: Bearer $PANEL_TOKEN"

# اعمال کانفیگ جدید (روی نود push و هسته restart می‌شود)
curl -X PUT http://PANEL_IP:8080/api/v1/nodes/<NODE_ID>/config \
  -H "Authorization: Bearer $PANEL_TOKEN" -H "Content-Type: application/json" \
  --data @config.json
```

### راه ۳ — مستقیم روی سرور نود
```bash
sudo pg-node-agent edit      # ویرایش fixed-config.json
sudo pg-node-agent restart   # اعمال
```

### راه ۴ — از پنل PasarGuard خودت با «core key» (مثل یک نود معمولی) ⭐
این راحت‌ترین راه برای مدیریت هسته است: نود را در یک پنل PasarGuard که **برای خودت** داری اضافه می‌کنی و از رابط گرافیکیِ همان پنل، کانفیگ هسته را ویرایش می‌کنی — دقیقاً مثل یک نود معمولی PasarGuard.

- نود **فقط از core key** کانفیگ هسته را می‌پذیرد؛ کلیدهای مشتری نمی‌توانند کانفیگ را عوض کنند (امن).
- core key **به‌صورت پیش‌فرض هنگام نصب ساخته و چاپ می‌شود** (در خروجی نصب: «Core key») و یک **UUID** است — چون پنل PasarGuard کلید API نود را حتماً به‌شکل UUID می‌پذیرد و هر چیز دیگری را رد می‌کند.
- اگر با نسخه‌ی قدیمی نصب کرده‌ای و core key فعلی‌ات UUID نیست (مثلاً رشته‌ی hex بلند)، یک UUID بساز و با `--core-key <UUID>` ست کن (فقط دوباره‌زدن `install` کلید قبلی را نگه می‌دارد و درستش نمی‌کند). برای ساخت UUID: `cat /proc/sys/kernel/random/uuid`.

در پنل PasarGuard خودت، نود را اضافه کن با:
| فیلد | مقدار |
|------|-------|
| Address / Port | `NODE_IP` + پورت gRPC (پیش‌فرض `62050`) |
| Protocol | gRPC |
| Certificate | گواهی نود |
| API Key | **core key** (نه master، نه کلید مشتری) |

سپس در همان پنل، Xray/Core آن نود را ویرایش کن و ذخیره؛ کانفیگ روی هسته‌ی مشترک اعمال و کاربرهای فعالِ مشتری‌ها دوباره اعمال می‌شوند. عملیات کاربر/Stop این اتصال نادیده گرفته می‌شود تا چند-مستأجری نشکند.

> فرق core key با master key: master برای **پنل اصلیِ فروش (NodePanel)** است و کل نود/مشتری‌ها را مدیریت می‌کند؛ core key فقط برای **مدیریت کانفیگ هسته از یک پنل PasarGuard** است. هر دو هم‌زمان کار می‌کنند.

## نمونه‌ی پروداکشن: VLESS + Reality (بدون دامنه/گواهی)
Reality برای ایران مناسب است و نیازی به دامنه یا گواهی ندارد. مراحل:

**۱) تولید کلید Reality روی سرور نود:**
```bash
/var/lib/pg-node-agent/xray-core/xray x25519
# خروجی:
# Private key: <PRIVATE_KEY>
# Public key:  <PUBLIC_KEY>
```
`Private key` در کانفیگ نود می‌رود؛ `Public key` را به مشتری می‌دهی (یا سیستم خودکار از روی private حساب و در «اطلاعات اتصال» نشان می‌دهد).

**۲) یک shortId بساز:** مثلاً `openssl rand -hex 8` → `0123456789abcdef`.

**۳) کانفیگ:**
```json
{
  "log": { "loglevel": "warning" },
  "inbounds": [
    {
      "tag": "vless-in",
      "listen": "0.0.0.0",
      "port": 443,
      "protocol": "vless",
      "settings": { "clients": [], "decryption": "none" },
      "streamSettings": {
        "network": "tcp",
        "security": "reality",
        "realitySettings": {
          "show": false,
          "dest": "www.microsoft.com:443",
          "xver": 0,
          "serverNames": ["www.microsoft.com"],
          "privateKey": "<PRIVATE_KEY>",
          "shortIds": ["", "0123456789abcdef"]
        }
      },
      "sniffing": { "enabled": true, "destOverride": ["http", "tls", "quic"] }
    }
  ],
  "outbounds": [ { "tag": "direct", "protocol": "freedom" } ]
}
```

> کاربرهای VLESS+Reality معمولاً `flow: xtls-rprx-vision` دارند؛ این مقدار را پنلِ مشتری روی کاربرهایش می‌گذارد و سیستم همان را اعمال می‌کند.

## نمونه‌ی جایگزین: VLESS + WebSocket + TLS (دامنه/CDN)
اگر دامنه و گواهی داری (مثلاً پشت Cloudflare):
```json
{
  "log": { "loglevel": "warning" },
  "inbounds": [
    {
      "tag": "vless-in",
      "listen": "0.0.0.0",
      "port": 443,
      "protocol": "vless",
      "settings": { "clients": [], "decryption": "none" },
      "streamSettings": {
        "network": "ws",
        "security": "tls",
        "wsSettings": { "path": "/vless" },
        "tlsSettings": {
          "serverName": "your.domain.com",
          "certificates": [
            { "certificateFile": "/etc/ssl/your.crt", "keyFile": "/etc/ssl/your.key" }
          ]
        }
      },
      "sniffing": { "enabled": true, "destOverride": ["http", "tls", "quic"] }
    }
  ],
  "outbounds": [ { "tag": "direct", "protocol": "freedom" } ]
}
```

## بعد از تنظیم: چه چیزی به مشتری بده؟
در پنل، روی اشتراکِ مشتری دکمه‌ی **«اتصال مشتری»** را بزن (یا `GET /api/v1/subscriptions/{id}/connection`). این‌ها را می‌گیری و به مشتری می‌دهی:
- **آدرس gRPC:** `NODE_IP:62050`
- **پروتکل:** gRPC
- **گواهی نود** (برای pin)
- **inboundهای نود:** همان JSON بالا — ولی **امن‌شده**: کلید خصوصی Reality حذف و **کلید عمومی** (`publicKey`) به‌جایش گذاشته می‌شود تا مشتری بتواند لینک بسازد.

مشتری در پنل PasarGuard خودش یک inbound با **همین** مقادیر می‌سازد:
| فیلد | باید برابر باشد با |
|------|--------------------|
| Port | `443` (یا پورت تو) |
| Protocol / Network | `vless` / `tcp` (یا `ws`) |
| Security | `reality` (یا `tls`) |
| SNI / serverNames | `www.microsoft.com` (یا دامنه‌ی تو) |
| Reality publicKey (pbk) | کلید عمومی نود |
| shortId (sid) | یکی از `shortIds` تو |
| flow | `xtls-rprx-vision` (برای Reality) |

اگر این‌ها نخوانند، لینک کاربر نهایی وصل نمی‌شود.

## چند inbound هم‌زمان
می‌توانی چند inbound تعریف کنی (مثلاً Reality روی ۴۴۳ و WS روی ۸۴۴۳). به‌صورت پیش‌فرض کاربرهای مشتری خودکار روی **همه‌ی** proxy inboundها اضافه می‌شوند. برای محدود کردن به زیرمجموعه، تگ‌های موردنظر را با کاما در `PG_AGENT_FORCE_INBOUNDS` بگذار:
```bash
sudo pg-node-agent edit-env
# PG_AGENT_FORCE_INBOUNDS=vless-in,vless-ws   # اختیاری؛ خالی = همه‌ی proxy inboundها
sudo pg-node-agent restart
```

## بررسی سلامت
```bash
sudo pg-node-agent status
sudo pg-node-agent logs        # خطاهای کانفیگ Xray اینجا دیده می‌شوند
```
یا از پنل: `GET /api/v1/nodes/{id}/health` → باید `core_started: true` باشد.

</div>
