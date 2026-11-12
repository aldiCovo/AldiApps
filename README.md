# AldiApps

Application source code, one folder per application. Each folder has its own `Dockerfile`. The Kubernetes manifests are in [`aldiCovo/AldiDevOps`](https://github.com/aldiCovo/AldiDevOps), in `apps/` under the same folder name.

| Folder | Application |
|---|---|
| [`cvaldi/`](cvaldi/) | Online CV (English / Indonesian), live at **https://aldilukito.aldiab.ai.id** |
| [`ecommerce/catalog-service/`](ecommerce/catalog-service/) | Go API for the shop's products and stock, using PostgreSQL. Only reachable inside the cluster |
| [`ecommerce/search-service/`](ecommerce/search-service/) | Go API that searches the shop's products, using Elasticsearch. Only reachable inside the cluster |
| [`ecommerce/user-service/`](ecommerce/user-service/) | Go API for the shop's accounts and login, using PostgreSQL. Only reachable inside the cluster |
| [`ecommerce/order-service/`](ecommerce/order-service/) | Go API for the shop's orders, which runs the order saga, using PostgreSQL. Only reachable inside the cluster |
| [`ecommerce/payment-service/`](ecommerce/payment-service/) | Go worker that simulates payments, using PostgreSQL. It has no public API and moves no money |
| [`ecommerce/realtime-service/`](ecommerce/realtime-service/) | Go service that tells browsers when an order changes, over WebSockets. Only reachable through web |
| [`ecommerce/notification-service/`](ecommerce/notification-service/) | Go worker that emails customers when their order finishes. It has no public API |
| [`ecommerce/cart-service/`](ecommerce/cart-service/) | Go API for each customer's cart, using PostgreSQL. Only reachable through `web` |
| [`ecommerce/chat-service/`](ecommerce/chat-service/) | Go API for the chat between customers and the shop's admins, using PostgreSQL. Reachable through `web` and admin-service |
| [`ecommerce/web/`](ecommerce/web/) | The shop's pages for customers, at **https://prastika.aldiab.ai.id** |
| [`ecommerce/admin-service/`](ecommerce/admin-service/) | Go API for the admin pages. It lets in admins only and calls the other services' internal routes. Only reachable through `web-admin` |
| [`ecommerce/web-admin/`](ecommerce/web-admin/) | The admin pages, with their own login, at **https://prastika.aldiab.ai.id/admin** |
| [`saham/signal-service/`](saham/signal-service/) | Go service that sends a daily LQ45 market brief to Telegram after the close, with a screener and forward paper trading of its rules, using its own PostgreSQL, and its web pages at **https://saham.aldiab.ai.id** behind a password. `signal-service backtest` replays the rules on past prices. Not investment advice |

## Deploy

Merge or push to `main`, then run this on a machine with Docker:

```bash
./build.sh cvaldi
./build.sh ecommerce/catalog-service
```

It pushes `ghcr.io/aldicovo/<folder name>:<commit>`, using the last commit that changed the folder, and writes that tag to the manifests. ArgoCD then deploys it.

A new image is private on GHCR, and the cluster pulls without credentials. After an application's first build, set its package to public in the package settings on GitHub.

## Metrics

Every Go service answers `GET /metrics` for Prometheus on port 9091, or `METRICS_PORT`. That port is separate from the main one, so no Service or Ingress reaches it, and only Prometheus reads it, through the Pod's address. It has:

| Metric | Labels |
|---|---|
| `http_requests_total` | `route`, the ServeMux pattern such as `GET /products/{id}` or `unmatched`, and `code` |
| `http_request_duration_seconds` | `route`. WebSockets aren't timed, because they last as long as the visitor stays |
| `go_*`, `process_*` | Memory, goroutines, garbage collection and CPU |

The same middleware logs every request as one JSON line with `msg` `request`, `method`, `path`, `route`, `status` and `ms`. A 5xx answer is logged at `ERROR` and anything else at `INFO`, so 5xx answers count toward the ErrorLogsSpike alert in AldiDevOps. The kubelet's `/healthz` and `/readyz` checks aren't logged. Only the path is written, cut at 200 characters, and never the query, which can hold a search or a token. The logs are kept in Loki and shown in Grafana's **Log** dashboard.

The code is `metrics.go` with `metrics_test.go`, the same in each service. Change all copies together.

## catalog-service

| Request | Does |
|---|---|
| `GET /categories` | Lists categories by name |
| `POST /categories` | Creates one from `{"name"}`. Answers 409 if the name exists, ignoring case |
| `GET /categories/{id}` | Returns one |
| `PUT /categories/{id}` | Replaces `name` |
| `DELETE /categories/{id}` | Deletes one. Answers 409 if it still has products |
| `GET /products?limit=20&offset=0&category_id=1` | Lists products with their variants. `category_id` is optional |
| `POST /products` | Creates one from `{"category_id", "name", "description", "price"}`. `name` and `price` are required |
| `GET /products/{id}` | Returns one with its variants |
| `PUT /products/{id}` | Replaces `category_id`, `name`, `description` and `price`. Leaving out `category_id` clears it. Answers 409 if `price` isn't above a sale that hasn't ended |
| `DELETE /products/{id}` | Deletes one and its variants |
| `PUT /products/{id}/sale` | Sells the product for less from `{"price", "starts_at", "ends_at"}`, replacing any sale it had. Times are RFC 3339 and `ends_at` must be in the future. Answers 409 unless `price` is below the regular price |
| `DELETE /products/{id}/sale` | Removes the sale. Answers the product |
| `POST /products/{id}/variants` | Creates a variant from `{"size", "color", "stock"}`. `size` and `color` are required. Answers 409 if the product already has that combination, ignoring case |
| `GET /variants/{id}` | Returns one |
| `PUT /variants/{id}` | Replaces `size` and `color` |
| `PATCH /variants/{id}/stock` | Adds `{"delta"}` to the stock. Answers 409 if the stock would go negative |
| `DELETE /variants/{id}` | Deletes one |

Prices are in whole rupiah and belong to the product. Stock belongs to each variant.

A product answers with `price`, what customers pay now, and `regular_price`. While a sale runs, `price` is the sale price and `original_price` is the regular price, which the shop shows struck through ("harga coret"). `sale` holds the sale as set, `{"price", "starts_at", "ends_at"}`, from before it starts until it is removed, even after it ended. The database's clock decides whether a sale runs, so orders, which read `price`, always charge what the product page shows. Every 30 seconds the service sends `product.updated` for each product whose sale started or ended, so search shows the new price within that time. A description holds at most 5000 characters. Migrations in `migrations/` run when the service starts.

Reading is open inside the cluster. Every change needs `Authorization: Bearer <INTERNAL_TOKEN>`, which only admin-service has, and the service won't start without a token of at least 32 characters. Orders change stock through events, not through these routes.

### Events

Every change is published to the `catalog.events` topic exchange on RabbitMQ, with the event type as the routing key:

| Type | `data` |
|---|---|
| `category.created`, `category.updated` | The category |
| `category.deleted` | `{"id"}` |
| `product.created`, `product.updated` | The whole product with its variants. Variant and stock changes send `product.updated` |
| `product.deleted` | `{"id"}` |
| `stock.reserved` | `{"order_id"}` |
| `stock.rejected` | `{"order_id", "reason"}`. `reason` is `out_of_stock` |

The message body is `{"id", "type", "occurred_at", "data"}`.

A change and its event commit in one transaction to the `outbox_events` table, and a background publisher sends them on and deletes them once RabbitMQ confirms. So the API keeps working while RabbitMQ is down, and the events follow when it returns.

Consumers must handle these:

- An event can arrive twice. `id` grows with every event, so compare it to ignore duplicates and anything older than what was already applied to the same product
- RabbitMQ drops events that no queue is bound for, including all events from before a consumer's queue existed. A new consumer must load the current state from the API first

### Stock reservations

catalog-service reads `order.created` and `order.cancelled` from `order.events`, through the `catalog-service.orders` queue. An order reserves all its items or none. Products are locked in id order, so two orders can't deadlock, and each `order_id` is only reserved once, so a repeated event changes nothing. `order.cancelled` returns the stock. Reserving or returning stock also sends `product.updated`, which keeps search current.

### Tests

The tests need a throwaway PostgreSQL, because they reset the database. The publisher's delivery test also needs RabbitMQ, and is skipped without `TEST_RABBITMQ_URL`:

```bash
cd ecommerce/catalog-service
TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:5432/catalog?sslmode=disable' \
TEST_RABBITMQ_URL='amqp://guest:guest@127.0.0.1:5672/' go test -race ./...
```

## search-service

| Request | Does |
|---|---|
| `GET /search?q=dress+merah` | Searches names, descriptions, sizes and colors. All words must match, and small typos are allowed |
| `GET /search?category_id=1` | Limits the results to one category |
| `GET /search?size=M&color=Merah&in_stock=true` | Limits the results to products with one variant that matches all of these |
| `GET /search?limit=20&offset=0` | Pages through the results. `limit` is at most 100 and `offset` at most 9900 |

The answer is `{"total", "products"}`, with products in the same shape as catalog-service's. All parameters are optional and combine. It answers 503 while Elasticsearch is down or before the first sync has created the index.

It binds its durable queue to `product.*` on `catalog.events`, loads every product from catalog-service, then applies events. Each event's `id` becomes the document's version in Elasticsearch, so duplicates and older events change nothing. Synced products use version 0, so they never replace an event's data. The sync runs at every start, so an emptied index rebuilds itself.

Category names aren't searchable, because the index only keeps `category_id`.

The tests need a throwaway Elasticsearch and RabbitMQ, because they delete their index and queue:

```bash
cd ecommerce/search-service
TEST_ELASTICSEARCH_URL='http://elastic:test@127.0.0.1:9200' \
TEST_RABBITMQ_URL='amqp://guest:guest@127.0.0.1:5672/' go test -race ./...
```

## user-service

| Request | Does |
|---|---|
| `POST /register` | Creates a customer from `{"email", "name", "password"}`. The password has 8 to 128 characters. Answers 409 if the email exists, ignoring case |
| `POST /login` | Checks `{"email", "password"}` and answers `{"token", "token_type", "expires_in", "user"}`, or 401 |
| `GET /me` | Returns the user that the `Authorization: Bearer <token>` header belongs to |
| `POST /password/forgot` | Emails a link for setting a new password to `{"email"}`, if it has an account. Always answers 204, before looking the email up, so it can't tell anyone which emails have accounts |
| `POST /password/reset` | Sets `{"password"}` with the `{"token"}` from a link, and answers 204. Answers 410 if the link is unknown, used or expired |
| `GET /.well-known/jwks.json` | Returns the public key that checks tokens |
| `GET /internal/users/{id}` | Returns one user, for other services. It needs `Authorization: Bearer <INTERNAL_TOKEN>`, and web has no route to it |
| `GET /internal/users?q=&role=&limit=&offset=` | Accounts, newest first, whose email or name contains `q`, with `role` `admin` or `customer` if given, as `{"users", "total"}`. `limit` is 1 to 100, 50 if left out. Needs `Authorization: Bearer <ADMIN_INTERNAL_TOKEN>` |
| `PUT /internal/users/{id}/role` | Sets `{"role"}` to `admin` or `customer`. Answers 409 rather than take the role from the last admin. Needs `ADMIN_INTERNAL_TOKEN` |

`ADMIN_INTERNAL_TOKEN` is only admin-service's, and must differ from `INTERNAL_TOKEN`, so a service that looks up customers can't make anyone an admin. A role change locks every admin's row first, so two admins taking each other's role at once still leave one.

A reset link is `SHOP_URL/#reset=<token>`. The token is after `#`, so the browser never sends it to a server or a log. Only its SHA-256 is stored. A link lasts 30 minutes and works once, and using one ends the account's other links. An account gets at most 3 links an hour, so the form can't fill someone's inbox. The email goes out after the answer, through `SMTP_ADDR` as `MAIL_FROM`, which is Mailpit for now. A new password doesn't end tokens already issued, which still last up to an hour.

Passwords are stored as Argon2id hashes. Only 3 are computed at once, which keeps a burst of logins inside the memory limit, and a request that waits over 5 seconds gets 503.

Tokens are JWTs signed with Ed25519 and last one hour. Their claims are `sub` (the user id), `role` (`customer` or `admin`), `iss` (`user-service`), `aud` (`shop`), `iat` and `exp`. Another service checks them with the public key, so it doesn't need a secret and can't create tokens. It must check the signature, `iss`, `aud` and `exp`, and only accept the `EdDSA` algorithm.

A token can't be cancelled, so it keeps working until it expires, even after a logout or a role change. `/me` reads the database, so it does notice a deleted account. There are no refresh tokens yet, so users log in again after an hour.

The tests need a throwaway PostgreSQL, because they empty the table:

```bash
cd ecommerce/user-service
TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:5432/users?sslmode=disable' go test -race ./...
```

## order-service

Every request needs the `Authorization: Bearer <token>` header from user-service, and a user only sees their own orders. Another user's order answers 404.

| Request | Does |
|---|---|
| `POST /orders` | Creates an order from `{"items": [{"variant_id", "quantity"}]}`, with 1 to 20 different variants and a quantity of 1 to 20 each, and an optional `voucher_code`. Answers 409 if the user already has 5 open orders, 422 if the voucher can't be used, and 503 while catalog-service is down |
| `POST /vouchers/check` | What `{"voucher_code", "items", "shipping_option", "province"}` would take off, with the voucher's terms, without holding it. The courier and province are optional; without them free shipping takes 0 so far |
| `GET /orders?limit=20&offset=0` | Lists the user's orders, newest first |
| `GET /orders/{id}` | Returns one |
| `POST /orders/{id}/pay` | Asks for a simulated payment with `{"simulate": "success"}` or `{"simulate": "failure"}`. Answers 202, or 409 if the order isn't waiting for payment |
| `POST /orders/{id}/cancel` | Cancels an order that is waiting for payment and returns its stock. Answers 409 in any other status |

Prices and names are copied from catalog-service when the order is created, so later catalog changes don't touch it.

#### Vouchers

A voucher takes a percentage of the items (`percent`, with an optional `max_discount`), an amount off the items (`amount`), or some or all of the shipping (`free_shipping`, with an optional `max_discount`). It never takes more than the items, or the shipping, cost. Each has a minimum subtotal, an optional total quota, a limit per account, a start and an end, and can be turned off.

The order keeps the code and the `discount`, and its `total` is the items plus shipping minus the discount. Creating the order locks the voucher's row, so orders made at the same moment can't pass its quota together. Only orders that still hold the voucher count: one that is rejected or cancelled gives its use back, to the quota and to the account.

A voucher that can't be used answers 422 with a `reason`: `not_found` (also for one that is turned off), `not_started`, `expired`, `used_up`, `already_used` or `min_subtotal`, which also gives `min_subtotal`. The shop words each one.

| Status | Means |
|---|---|
| `pending` | Waiting for catalog-service to reserve the stock |
| `awaiting_payment` | Stock is held until `expires_at`, which is `PAYMENT_WINDOW` (default `15m`) after the reservation |
| `paying` | Waiting for payment-service. It can no longer expire, so a slow payment-service can't cancel an order that is being paid |
| `paid` | Paid. `paid_at` says when |
| `packed` | Packed by the shop, from the admin pages |
| `shipped` | Handed to the courier. `tracking_number` holds the courier's number |
| `completed` | Delivered. Each of these three steps keeps its time, in `packed_at`, `shipped_at` and `completed_at` |
| `rejected` | Not enough stock. `reason` is `out_of_stock` |
| `cancelled` | Stock was returned. `reason` is `payment_failed`, `expired` or `cancelled_by_customer` |

#### Internal routes for the admin pages

These need `Authorization: Bearer <INTERNAL_TOKEN>` instead of a login token, and see every customer's orders with their `user_id`. Only admin-service calls them, and `web` never passes `/internal`.

| Request | Does |
|---|---|
| `GET /internal/orders?status=paid&limit=20&offset=0` | Lists orders, newest first, optionally with one status |
| `GET /internal/orders/{id}` | Returns one |
| `GET /internal/vouchers` | Every voucher, newest first, each with `used`: the orders holding it now |
| `POST /internal/vouchers`, `PUT /internal/vouchers/{id}` | Makes or changes a voucher from `{"code", "description", "kind", "value", "max_discount", "min_subtotal", "quota", "per_user_limit", "starts_at", "ends_at", "active"}`. A code is 3 to 20 letters or digits, stored in capitals, and answers 409 if another voucher has it. Orders keep the code they were made with |
| `DELETE /internal/vouchers/{id}` | Deletes a voucher no order has used. One that was used answers 409 and can only be turned off |
| `POST /internal/orders/{id}/status` | Moves a paid order one step on with `{"status": "packed"}`, `{"status": "shipped", "tracking_number": "JP1234567890"}` or `{"status": "completed"}`. A tracking number is 6 to 40 letters, digits or dashes, and is stored in capitals. Answers 409 if the order isn't in the step before, so a step can't be skipped or done twice |

Shipping and completing send `order.shipped` and `order.completed`, for the customer's email. Every step sends the customer's browser a signal.

### The order saga

No service reads another's database. Each step commits its change and its event in one transaction, using the same outbox as catalog-service:

1. order-service saves a `pending` order and sends `order.created`
2. catalog-service reserves every item or none, and sends `stock.reserved` or `stock.rejected`
3. order-service moves to `awaiting_payment` or `rejected`
4. `POST /orders/{id}/pay` moves to `paying` and sends `payment.requested`
5. payment-service records the result and sends `payment.succeeded` or `payment.failed`
6. order-service moves to `paid`. On a failure it moves to `cancelled` and sends `order.cancelled`, and catalog-service returns the stock

A loop checks every 5 seconds for orders past `expires_at`, cancels them and sends `order.cancelled`.

After every status change commits, it publishes `{"order_id", "status"}` to `orders.<user id>` on NATS, for realtime-service. These signals may be lost, so a NATS outage never stops an order, and a repeated event sends nothing.

| Event | Exchange | `data` |
|---|---|---|
| `order.created` | `order.events` | `{"order_id", "items": [{"variant_id", "quantity"}]}` |
| `order.cancelled` | `order.events` | `{"order_id", "user_id", "reason", "total", "items"}`, and `discount` and `voucher_code` when a voucher was used |
| `payment.requested` | `order.events` | `{"order_id", "amount", "outcome"}` |
| `order.paid`, `order.rejected` | `order.events` | `{"order_id", "user_id", "reason", "total", "items"}`, for its email |

Consumers ignore an event that doesn't match the order's current status, so a duplicate changes nothing.

It reads the public key from user-service's JWKS, and keeps it in memory.

The tests need a throwaway PostgreSQL, because they reset the database:

```bash
cd ecommerce/order-service
TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:5432/orders?sslmode=disable' go test -race ./...
```

## payment-service

It has no public API, and no money moves. It takes `payment.requested` from `order.events`, stores one row per `order_id` in `payments`, and sends the outcome that the order asked for to `payment.events`. The unique `order_id` makes a repeated request send nothing new.

| Event | `data` |
|---|---|
| `payment.succeeded` | `{"order_id"}` |
| `payment.failed` | `{"order_id", "reason"}` |

The tests need a throwaway PostgreSQL, because they reset the database:

```bash
cd ecommerce/payment-service
TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:5432/payments?sslmode=disable' go test -race ./...
```

## notification-service

It has no public API. It reads `order.paid`, `order.rejected`, `order.cancelled`, `order.shipped` and `order.completed` from `order.events`, through the `notification-service.orders` queue, and sends one email for each in Indonesian. It reads the address from user-service when it needs it, so it keeps no copy.

An event stays in the durable queue until its email is sent, so an email waits while the mail server or user-service is down, and also holds back the emails behind it. A row in `sent_notifications` stops a repeated event from sending again. A crash between sending and saving that row sends one email twice, which is better than losing it. An unreadable event, a deleted user, or an address the server refuses for good is logged and dropped.

In the cluster it sends to Mailpit, which delivers nothing, so customers don't receive these emails. Sending for real needs a mail provider that accepts a login, which `SMTPMailer` doesn't do yet.

The tests need a throwaway PostgreSQL, because they empty the table:

```bash
cd ecommerce/notification-service
TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:5432/notification?sslmode=disable' go test -race ./...
```

## cart-service

Keeps each customer's cart on their account, so it follows them to any browser. Every request needs a login token, and only reaches that customer's own cart.

| Request | Does |
|---|---|
| `GET /cart` | Returns `{"items": [{"product_id", "variant_id", "quantity"}], "skipped": 0}` |
| `POST /cart/merge` | Adds `{"items": [...]}`, summing a variant that's already there up to 20. New variants beyond the limit of 20 are counted in `skipped` |
| `PUT /cart/items/{variant_id}` | Sets `{"product_id", "quantity"}`. Answers 409 if it's a new variant and the cart is full |
| `DELETE /cart/items/{variant_id}` | Removes one variant |
| `DELETE /cart` | Empties the cart |

It holds only ids and quantities, because names, prices and stock belong to catalog-service and can change. The limits match an order's, so everything in a cart can be ordered at once. It doesn't check that a variant exists: the pages remove unknown ones when the cart opens, and order-service checks again.

It needs `DATABASE_URL` and `USER_URL`, and won't start without them. The tests need a throwaway PostgreSQL, because they empty the table:

```bash
cd ecommerce/cart-service
TEST_DATABASE_URL='postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable' go test -race ./...
```

## admin-service

The only way in for the admin pages. It has no database. Every request except `POST /login` needs `Authorization: Bearer <token>` from an admin login, and admin-service asks user-service's `/me` each time, so an account that loses the admin role, or is deleted, is refused at once.

| Request | Does |
|---|---|
| `POST /login` | Logs in through user-service with `{"email", "password"}`, and answers 403 unless the account is an admin |
| `GET /me` | Returns the admin |
| `GET /orders?status=&limit=&offset=` | Every customer's orders from order-service, each with `customer`: `{"name", "email"}`, or `null` for a deleted account |
| `GET /orders/{id}` | One order, the same way |
| `POST /orders/{id}/status` | Moves it a step on, as order-service's internal route does |
| `GET`, `POST /categories`, `PUT`, `DELETE /categories/{id}` | catalog-service's category routes |
| `GET`, `POST /products`, `GET`, `PUT`, `DELETE /products/{id}`, `PUT`, `DELETE /products/{id}/sale`, `POST /products/{id}/variants` | catalog-service's product and sale routes |
| `PUT`, `DELETE /variants/{id}`, `PATCH /variants/{id}/stock` | catalog-service's variant and stock routes |
| `GET /photos/products` | Every product's four photo places, by product id, `""` where empty |
| `PUT /photos/products/{id}/{1-4}` | Puts the photo in the body in that place, replacing what was there. Answers the product's four places |
| `DELETE /photos/products/{id}/{1-4}` | Removes that photo |
| `POST /photos/products/{id}/{1-4}/first` | Swaps that photo with the first, which the shop shows on the product's card |
| `GET /photos/categories` | Each category's photo, by category id |
| `PUT`, `DELETE /photos/categories/{id}` | Puts or removes a category's photo |
| `GET /users?q=&role=&limit=&offset=` | Accounts from user-service, as its `/internal/users` |
| `PUT /users/{id}/role` | Makes an account an admin or a customer with `{"role"}`. An admin can't change their own role (409), and user-service keeps at least one admin |
| `GET`, `POST /vouchers`, `PUT`, `DELETE /vouchers/{id}` | order-service's voucher routes |

catalog-service checks the changes itself, and admin-service passes its answers on, except that a refused internal token answers 502, so it can't look like a logged-out admin. Every change is logged with the admin's id.

Photos go to `PHOTOS_DIR`, the same folder on AldiServer that `web` serves them from, so a new photo shows in the shop at once: `products/<id>/1.jpg` to `4.jpg`, and `categories/<name>.jpg`, where the name is the category's in lowercase with dashes for spaces, such as `office-on-style.jpg`. A photo is at most 5 MB and must be a JPEG, PNG or WEBP by its content, whatever the request says, and JPEG and PNG must decode to at most 10000 pixels a side. Each is written to a temporary file and renamed into place, so the shop never serves half a photo, and links in the folder are never followed. Deleting a product removes its photos, deleting a category removes its photo, and renaming a category moves its photo to the new name. A category whose name has other characters, such as `&`, can't have a photo, because the shop can't find it.

It needs `USER_URL`, `ORDER_URL`, `CATALOG_URL`, `USER_INTERNAL_TOKEN`, `ORDER_INTERNAL_TOKEN`, `CATALOG_INTERNAL_TOKEN`, `USER_ADMIN_TOKEN` (user-service's `ADMIN_INTERNAL_TOKEN`) and `PHOTOS_DIR`. The tests use fake services:

```bash
cd ecommerce/admin-service
go test -race ./...
```

## web-admin

The admin pages, under `/admin` on the shop's domain, separate from `web`. Its login cookie, `admin_session`, is HttpOnly, `SameSite=Strict` and only sent to `/admin/api`, so the shop's cookie never reaches these pages, nor this one the shop. It passes only these requests to admin-service:

| Request | Goes to |
|---|---|
| `POST /admin/api/login`, `POST /admin/api/logout` | admin-service, then sets or clears the cookie |
| `GET /admin/api/me`, `GET /admin/api/orders`, `GET /admin/api/orders/{id}` | admin-service, with the token from the cookie |
| `POST /admin/api/orders/{id}/status` | admin-service, with the token from the cookie |
| The category, product, variant and stock routes above, under `/admin/api` | admin-service, with the token from the cookie. Every change, including `DELETE`, must be sent as JSON |
| `GET /admin/api/users`, `PUT /admin/api/users/{id}/role` | admin-service, with the token from the cookie. A role change must be sent as JSON |
| `GET`, `POST /admin/api/vouchers`, `PUT`, `DELETE /admin/api/vouchers/{id}` | admin-service, with the token from the cookie. Every change must be sent as JSON |
| The photo routes above, under `/admin/api` | admin-service, with the token from the cookie. An upload must be sent as `image/jpeg`, `image/png` or `image/webp`, which a form on another site can't send, and other changes as JSON |

The *Produk* page (`/admin/#produk`) manages categories, products with their price, category and description, and each product's variants. Stock changes by an amount added or taken away, not by a new total, so orders placed meanwhile aren't overwritten. Deleting a product or variant asks first, and old orders keep their own copy of names and prices. *Atur harga coret* gives a product a sale price from a start to an end time; the page shows whether it hasn't started, runs or ended, and *Hapus harga coret* removes it. The product form changes the regular price.

Each product has four photo places; *Jadikan utama* makes a photo the first, the one on the shop's card. *Kelola kategori* gives each category its photo. Before uploading, the browser turns a photo into a JPEG of at most 1600 pixels a side, which keeps uploads small and drops the location a phone camera writes into it.

The *Pengguna* page (`/admin/#pengguna`) lists accounts, newest first, with a search by name or email and tabs for admins and customers. *Jadikan admin* and *Cabut admin* ask first. A demoted admin is out on their next request, and their own account shows no button.

The *Voucher* page (`/admin/#voucher`) makes, changes, turns off and deletes vouchers, and shows how many orders hold each. Only an unused voucher can be deleted, after asking first.

The *Pesanan* page lists orders by step: *Perlu dikemas* (paid), *Dikemas*, *Dikirim*, *Selesai* and *Semua*. Each order offers only its next step, and shipping asks for the tracking number. It needs `ADMIN_URL`.

## chat-service

Keeps one conversation per customer with the shop's admins. Customers reach it through web with their login token, and only ever see their own. Admins reach it through admin-service with the internal token.

| Request | Does |
|---|---|
| `GET /chat?before=&limit=` | The customer's newest messages, oldest first, as `{"messages": [{"id", "from", "body", "attachment", "created_at"}], "has_more", "read_up_to", "unread"}`. `from` is `customer` or `admin`; `read_up_to` is the newest message the shop has seen |
| `GET /chat/unread` | How many of the shop's messages the customer hasn't seen |
| `POST /chat/messages` | Sends `{"body", "attach"}`: up to 1,000 characters, and optionally `{"kind": "product" or "order", "id"}`. The text may be empty with a card. Answers 422 for a product that is gone or an order that isn't the customer's, 502 when catalog or orders don't answer, and 429 above 20 a minute |
| `POST /chat/read` | Marks every message so far as seen by the customer |
| `GET /internal/conversations?filter=waiting\|all&limit=&offset=` | Conversations, newest first, with `waiting` (the customer wrote last) and `unread` |
| `GET /internal/chat/waiting` | How many conversations wait for an answer |
| `GET /internal/conversations/{user_id}/messages?before=&limit=` | One conversation, as the shop sees it |
| `POST /internal/conversations/{user_id}/messages` | Answers with `{"body", "admin_id", "attach"}`; admins attach only products. Admins can't start a conversation |
| `POST /internal/conversations/{user_id}/read` | Marks the customer's messages as seen by the shop |

A card (`attachment`) is kept as it was when sent: a product's `name`, `price` and `category_id` from catalog-service, or an order's `status`, `total`, first item `name` and how many `more` items from order-service. Orders are looked up with the customer's own token, so order-service itself refuses another customer's order.

It keeps only user ids, so admin-service adds names and emails. After each message it publishes `{"type": "chat", "message_id"}` to `chat.<user id>` on NATS, so the customer's open chat updates at once; a lost signal only waits for the next poll. Admins get no signal: their page asks again every 5 seconds while open.

It needs `DATABASE_URL`, `USER_URL`, `CATALOG_URL`, `ORDER_URL`, `NATS_URL` and `INTERNAL_TOKEN` (at least 32 characters). The tests need a throwaway PostgreSQL, because they empty the tables:

```bash
cd ecommerce/chat-service
TEST_DATABASE_URL='postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable' go test -race ./...
```

## realtime-service

| Request | Does |
|---|---|
| `GET /ws` | Opens a WebSocket for the user in the `Authorization: Bearer <token>` header. Answers 401 without a valid token, 429 above 5 connections per user, and 503 above 500 in total |

It subscribes to `orders.*` and `chat.*` on NATS and sends each signal only to its owner's connections, as `{"order_id", "status"}` or `{"type": "chat", "message_id"}`. It has no database, and a signal only says that something changed, so the browser reads the order from order-service.

Core NATS keeps nothing, so a signal is lost if nobody is connected. The browser handles that by reloading open orders whenever the socket opens, and by polling unfinished orders: every 15 seconds while connected, and every 1.5 seconds while not.

A connection closes when its token expires, and also if it falls 16 signals behind, because reconnecting catches up. It sends a ping every 30 seconds, which keeps quiet connections open through Cloudflare and Traefik.

`/readyz` answers 503 while NATS is disconnected. The tests use a fake token check. The test that sends through NATS is skipped without `TEST_NATS_URL`:

```bash
cd ecommerce/realtime-service
TEST_NATS_URL='nats://127.0.0.1:4222' go test -race ./...
```

## web

Serves the pages in `static/`, and passes on only these requests:

| Request | Goes to |
|---|---|
| `GET /api/categories`, `GET /api/products/{id}` | catalog-service |
| `GET /api/search` | search-service |
| `POST /api/register`, `POST /api/login`, `GET /api/me`, `POST /api/password/forgot`, `POST /api/password/reset` | user-service |
| `POST /api/logout` | Handled here. It only clears the cookie |
| `GET /api/events` | realtime-service, as a WebSocket with the token from the cookie. Answers 403 if `Origin` isn't this site |
| `POST /api/orders`, `GET /api/orders`, `GET /api/orders/{id}`, `POST /api/orders/{id}/pay` | order-service, with the token from the cookie |
| `POST /api/vouchers/check` | order-service, with the token from the cookie |
| `GET` and `DELETE /api/cart`, `POST /api/cart/merge`, `PUT` and `DELETE /api/cart/items/{id}` | cart-service, with the token from the cookie |
| `GET /api/chat`, `GET /api/chat/unread`, `POST /api/chat/messages`, `POST /api/chat/read` | chat-service, with the token from the cookie |

Anything else answers 404, so the other services' write endpoints stay inside the cluster, and a new endpoint stays private until it's added here.

Login keeps the token in an `HttpOnly`, `Secure`, `SameSite=Lax` cookie, so page scripts can't read it. Because of `Secure`, browsers only keep it on HTTPS and `localhost`. `POST` requests must be `application/json`, which a form on another site can't send without asking. A WebSocket handshake carries the cookie without asking, so `/api/events` checks `Origin` instead.

A guest's cart lives in the browser's `localStorage`, so it needs no account. A customer's lives in cart-service, so it follows them to any browser and leaves the browser when they log out. Logging in moves a guest's cart to the account. The browser's copy stays until the account has it. Either way the cart holds only ids and quantities, and reads names, prices and stock again each time it opens. Checkout sends one `POST /api/orders`, which holds at most 20 different variants, up to 20 of each. An order is all or nothing: if one item is short, the whole order is rejected, and the customer can put it back in the cart. At checkout a voucher code is checked with `/api/vouchers/check`, and the discount follows the chosen courier; order-service checks it again when the order is made, and if it stopped being usable meanwhile, nothing is ordered and the checkout says why.

Photos come from `PHOTOS_DIR`, which the admin pages fill. A product without photos shows its category's photo, and a category without one gets a drawn garment for Dress, Blouse, Rok and Hijab, or the first letter of its name. The heading font is Cormorant Garamond, served from `static/fonts/` under its [license](ecommerce/web/static/fonts/OFL.txt), because the Content-Security-Policy only allows this site's own files.

It needs `CATALOG_URL`, `SEARCH_URL`, `USER_URL`, `ORDER_URL`, `REALTIME_URL` and `CART_URL`, and won't start without them.

The tests use fake services, so they need nothing running:

```bash
cd ecommerce/web
go test -race ./...
```
