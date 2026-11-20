# ADR 0008: Admin isolation and SSRF defence

## Статус

Принято.

## Контекст

Admin API управляет провайдерами и их URL. Если admin-контур доступен из
публичной сети или URL не валидируется, это открывает путь к SSRF и компрометации
control plane.

## Решение

- Отдельный listener (`GOGATE_ADMIN_ADDR`) и отдельный роутер.
- Отдельный статический Bearer-токен, сравнение constant-time.
- Публичный listener не содержит admin routes.
- URL провайдера задаёт только admin.
- Валидация URL: http/https, без userinfo, DNS-резолвинг, запрет metadata,
  link-local, multicast, unspecified; private/loopback только по флагу.
- HTTP-клиент не следует redirect'ам.
- pprof только на admin и по флагу.
- Секреты провайдеров — env, в БД только имя переменной.

## Последствия

- Admin-порт нельзя публиковать в интернет; используйте VPN/bastion.
- Локальный Ollama требует `GOGATE_ALLOW_PRIVATE_UPSTREAMS=true`.
- DNS rebinding остаётся остаточным риском; нужен egress firewall.
