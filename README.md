# NodeAgent — PasarGuard multi-tenant & dedicated node (fork)

نودِ **چند-مستأجری و هسته اختصاصی** بر پایه‌ی فورک [`PasarGuard/node`](https://github.com/PasarGuard/node). پشتیبانی هم‌زمان از **هسته اشتراکی** (چند مشتری با یک هسته Xray مشترک) و **هسته‌های اختصاصی** (پروسس مستقل Xray روی پورت‌های پویا با مسیریابی SNI روی پورت 443)، سهمیه‌بندی حجم و انقضا به‌صورت **محلی**، سقف مجاز کاربران (Hard Cap)، و فیلتر اینباندهای مجاز.

> Repo: `https://github.com/loopy-iri/NodeAgent`  
> پنلِ کنترل در ریپوی جدا: `https://github.com/loopy-iri/NodePanel`  
> 📖 **راهنمای کامل (نصب، فروش، اتصال مشتری، API):** پوشه‌ی [`docs/wiki/`](docs/wiki/Home.md)

---

## معماری هسته اشتراکی و اختصاصی (Architecture)

![معماری هسته اشتراکی و اختصاصی](docs/images/architecture.svg)

---

## کنترل دسترسی‌ها و سقف ظرفیت کاربران (Governance & Permissions)

![کنترل دسترسی خریدار و هشدارهای وب‌هوک](docs/images/buyer-permissions.svg)

---

## ویژگی‌ها
- **تفکیک هسته اشتراکی (Shared) و اختصاصی (Dedicated):**
  - **اشتراکی:** سبک، بدون مصرف بالای رم، چندین خریدار روی یک پروسس Xray با تفکیک ایمیل کاربران.
  - **اختصاصی:** هر خریدار دارای پروسس مستقل Xray روی پورت‌های پویا (`11000+`). خریدار می‌تواند کانفیگ، روتینگ و اوت‌باندهای خود را مستقیماً از پنل PasarGuard اعمال کند (`Start/Restart`). ترافیک روی پورت 443 از طریق **SNI Dispatcher** بر اساس دامنه‌های خریدار تفکیک می‌شود.
- **فیلتر اینباندهای مجاز (`allowed_inbounds`):** کاربرهای خریدار فقط روی اینباندهای تعیین‌شده توسط اپراتور اعمال می‌شوند. سایر اینباندها مخفی و مسدود هستند.
- **اعمال سقف سخت کاربران (`max_users`):** در صورت تکمیل ظرفیت اسلات‌های مجاز، درخواست‌های ساخت کاربر از پنل مشتری با خطای `HTTP 400 Bad Request` و gRPC `codes.ResourceExhausted` پس زده می‌شوند.
- **یکتایی credential** (uuid/password) در کل نود enforce می‌شود (ایزولاسیون امن auth).
- **enforcement محلی**: اتمام حجم/انقضا → کاربرهای مشتری از هسته حذف و کلیدش رد می‌شود (suspend ≠ delete؛ تمدید، کاربرها را برمی‌گرداند).
- **TLS سلف‌ساین** (auto-gen) + pin گواهی توسط پنل (یا TOFU خودکار).
- **gRPC سازگار با PasarGuard** (پورت `62050`): مشتری‌ها با کلید خودشان وصل می‌شوند و کاربر می‌سازند؛ اپراتور نیز با **core key** می‌تواند کانفیگ هسته را کنترل کند.

## اجرا

```bash
# نصب کامل (دانلود باینری per-arch + Xray + systemd؛ بدون build روی سرور):
sudo bash -c "$(curl -sL https://raw.githubusercontent.com/loopy-iri/NodeAgent/main/scripts/pg-node.sh)" @ install

# یا از روی clone:
sudo bash scripts/pg-node.sh install
```

دستورهای CLI: `install, update [VER], uninstall, up, down, restart, status, logs, core-update [VER], renew-cert, edit, edit-env, install-script, completion`.

> نصب از **باینری از‌پیش‌ساخته** (GitHub Releases) انجام می‌شود؛ برای ساخت باینری‌ها کافی است یک تگ `v*` push کنی تا workflow ریلیز اجرا شود. اجرا با **systemd** (سرویس `pg-node-agent`) است، بدون Docker و بدون کامپایل روی سرور. هنگام نصب، master key و core key و گواهی نود چاپ می‌شوند.

### اجرای محلی (توسعه)
```bash
$env:PG_AGENT_MASTER_KEY="master"; $env:PG_AGENT_FIXED_CONFIG="configs/fixed-config.example.json"
$env:XRAY_EXECUTABLE_PATH="<path>/xray"; go run ./cmd/agent
```

## متغیرهای محیطی

| متغیر | پیش‌فرض | شرح |
|---|---|---|
| `PG_AGENT_HTTP_ADDR` | `:8090` | آدرس API کنترلی (HTTPS) |
| `PG_AGENT_MASTER_KEY` | — (الزامی) | کلید مستر برای پنل اصلی |
| `PG_AGENT_CORE_KEY` | تولید خودکار | کلید مدیریت کانفیگ هسته از پنل PasarGuard (gRPC همیشه فعال است) |
| `PG_AGENT_GRPC_ADDR` | `:62050` | آدرس gRPC |
| `PG_AGENT_FORCE_INBOUNDS` | `vless-in` | کاربرهای هر مشتری روی این inbound tag(ها) اعمال شوند |
| `PG_AGENT_TENANT_DB` | `tenants.bolt` | مسیر store محلی bbolt |
| `PG_AGENT_FIXED_CONFIG` | — | کانفیگ ثابت Xray |
| `SSL_CERT_FILE`/`SSL_KEY_FILE` | `/var/lib/pg-node/certs/...` | گواهی (auto-gen اگر نبود) |

## اتصال مشتری از پنل PasarGuard

مشتری (که پنل PasarGuard خودش را دارد) نود تو را مثل یک **نود معمولی** اضافه می‌کند:
- **Address/Port:** آی‌پی نود + پورت gRPC (پیش‌فرض `62050`).
- **Protocol:** gRPC.
- **Certificate:** گواهی نود (همان PEM که موقع نصب چاپ شد / در پنل اصلی نمایش داده می‌شود) — برای pin.
- **API Key:** **کلید مشتری** که پنل اصلیِ تو هنگام ساخت اشتراک تولید کرده (نه master/core key).

سپس:
- کاربرهای پنل PasarGuard مشتری (vless/vmess/...) روی نود زیر همان tenant اضافه می‌شوند.
- در حالت هسته اشتراکی، کانفیگ هسته خریدار ایگنور شده و فقط کاربرها روی اینباندهای مجاز اعمال می‌شوند.
- در حالت هسته اختصاصی، کانفیگ دلخواه خریدار روی اینستنس مستقل او اعمال و دامنه‌هایش از پورت 443 به اینستنس هدایت می‌شوند.
- با اتمام حجم/انقضا، کاربرهای آن مشتری حذف و دسترسی کلیدش رد می‌شود.

## ساختار
```
cmd/agent/            entrypoint چند-مستأجری
tenant/               Registry + auth دو سطحی + enforcement (bbolt) + limits
shared/               مدیر هسته مشترک + instance_manager (هسته‌های اختصاصی + SNI router)
controller/agent/     API HTTP (master/tenant) + حلقه‌ی enforcement
controller/grpccompat/ gRPC سازگار با PasarGuard (مدیریت هسته اختصاصی/اشتراکی)
backend/, common/, ... کد پایه‌ی upstream
```
