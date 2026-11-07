<div dir="rtl">

[🇬🇧 English](07-Update-Maintenance-EN) · **🇮🇷 فارسی**

# آپدیت و نگهداری (اپراتور)

## آپدیت
از نسخه‌ی v0.2.2 به بعد، دستور `update` هم **باینری** و هم **خودِ اسکریپت CLI** را تازه می‌کند:
```bash
sudo pg-node-agent update     # روی هر نود
sudo pg-panel update          # روی پنل
```
کلیدها، گواهی، کانفیگ و دیتابیس همه حفظ می‌شوند.

نسخه‌ی مشخص:
```bash
sudo pg-node-agent update v0.2.2
```

## آپدیت و مدیریت هسته از پنل وب (بدون SSH)
از نسخه‌ی نود v0.5.0 و پنل v0.6.0 به بعد، از **پنل → نودها → جزئیات** می‌توانی بدون SSH:
- هسته را **شروع/ری‌استارت/توقف** کنی،
- **نسخه‌ی Xray** را عوض کنی (نود خودش دانلود و ری‌استارت می‌کند)،
- **باینری نود را آپدیت** کنی (مثل اجرای اسکریپت).

> نکته: «آپدیت نود از پنل» فقط برای نودهایی کار می‌کند که از قبل **v0.5.0 به بالا** باشند. اولین‌بار نود را یک‌بار با اسکریپت آپدیت کن (`sudo pg-node-agent update`)، از آن به بعد از پنل هم می‌شود.

> اگر نسخه‌ی نصب‌شده‌ات قدیمی‌تر از v0.2.1 است (یعنی هنوز self-update ندارد)، **یک‌بار** اسکریپت را با اجرای دوباره‌ی `install` تازه کن (کلیدها/دیتا حفظ می‌شوند):
> ```bash
> sudo bash -c "$(curl -sL https://raw.githubusercontent.com/l8PYGIT/NodeAgent/main/scripts/pg-node.sh)" @ install
> sudo bash -c "$(curl -sL https://raw.githubusercontent.com/l8PYGIT/NodePanel/main/scripts/pg-panel.sh)" @ install
> ```

## آپدیت هسته‌ی Xray
```bash
sudo pg-node-agent core-update          # آخرین نسخه
sudo pg-node-agent core-update v1.8.23   # نسخه‌ی مشخص
```

## تمدید گواهی نود
```bash
sudo pg-node-agent renew-cert
```
بعدش گواهی جدید را در پنل اصلی دوباره pin کن (یا اگر TOFU است، رکورد قبلی را پاک کن تا دوباره گرفته شود).

## پشتیبان‌گیری
- نود: کل `/var/lib/pg-node-agent/` (شامل `tenants.bolt`, `certs/`, `fixed-config.json`).
- پنل: فایل دیتابیس `/var/lib/pg-panel/panel.db` و `/opt/pg-panel/.env` (شامل توکن).

## نصب کنار نود رسمی PasarGuard
با `--name` مسیر/سرویس/CLI جدا می‌شود:
```bash
sudo bash -c "$(curl -sL .../pg-node.sh)" @ install --name pg-node-agent
```
سپس مدیریت با همان نام: `sudo pg-node-agent status`.

## حذف
```bash
sudo pg-node-agent uninstall   # دیتا در /var/lib/... می‌ماند (دستی پاک کن)
sudo pg-panel uninstall
```

</div>
