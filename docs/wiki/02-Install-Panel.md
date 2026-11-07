<div dir="rtl">

[🇬🇧 English](02-Install-Panel-EN) · **🇮🇷 فارسی**

# ۱) نصب پنل (اپراتور)

پنل را روی یک سرور لینوکسی (با systemd) نصب کن. باینری آماده دانلود می‌شود؛ نیازی به Docker یا کامپایل نیست.

## نصب یک‌خطی
```bash
sudo bash -c "$(curl -sL https://raw.githubusercontent.com/l8PYGIT/NodePanel/main/scripts/pg-panel.sh)" @ install
```

در پایان این‌ها چاپ می‌شود — **یادداشت کن**:
```
Panel UI:   http://SERVER_IP:8080/
API docs:   http://SERVER_IP:8080/docs
API token (Bearer) for the sales bot:
    <TOKEN>
```

## گزینه‌ها
| گزینه | پیش‌فرض | توضیح |
|------|---------|-------|
| `--port PORT` | `8080` | پورت HTTP پنل |
| `--token TOKEN` | تصادفی | توکن Bearer برای API/ربات |
| `--version vX.Y.Z` | latest | نصب نسخه‌ی مشخص |
| `--name NAME` | `pg-panel` | نام نمونه (مسیر/سرویس/CLI) — برای نصب کنار سرویس دیگر |

## نکته‌ی امنیتی مهم
پنل به‌صورت HTTP و بدون TLS بالا می‌آید. **قبل از قرار دادن روی اینترنت، پشت یک reverse proxy با TLS** (مثلاً Caddy یا Nginx) بگذارش و توکن را محرمانه نگه دار. توکن دسترسی کامل مدیریتی می‌دهد.

## دستورهای مدیریتی
```bash
sudo pg-panel status      # وضعیت سرویس
sudo pg-panel logs        # لاگ زنده
sudo pg-panel restart
sudo pg-panel info        # نمایش URL/docs/token
sudo pg-panel set-token   # چرخش توکن (تصادفی) — یا: set-token <TOKEN>
sudo pg-panel update      # آپدیت به آخرین نسخه
```

## مرحله‌ی بعد
ورود به `http://SERVER_IP:8080/` و سپس [نصب نود](03-Install-Node).

</div>
