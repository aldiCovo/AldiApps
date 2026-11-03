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
| [`ecommerce/web/`](ecommerce/web/) | The shop's pages for customers, at **https://prastika.aldiab.ai.id** |
| [`ecommerce/admin-service/`](ecommerce/admin-service/) | Go API for the admin pages. It lets in admins only and calls the other services' internal routes. Only reachable through `web-admin` |
| [`ecommerce/web-admin/`](ecommerce/web-admin/) | The admin pages, with their own login, at **https://prastika.aldiab.ai.id/admin** |

## Deploy

Merge or push to `main`, then run this on a machine with Docker:

```bash
./build.sh cvaldi
./build.sh ecommerce/catalog-service
```

It pushes `ghcr.io/aldicovo/<folder name>:<commit>`, using the last commit that changed the folder, and writes that tag to the manifests. ArgoCD then deploys it.

A new image is private on GHCR, and the cluster pulls without credentials. After an application's first build, set its package to public in the package settings on GitHub.

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
| `PUT /products/{id}` | Replaces `category_id`, `name`, `description` and `price`. Leaving out `category_id` clears it |
| `DELETE /products/{id}` | Deletes one and its variants |
| `POST /products/{id}/variants` | Creates a variant from `{"size", "color", "stock"}`. `size` and `color` are required. Answers 409 if the product already has that combination, ignoring case |
| `GET /variants/{id}` | Returns one |
| `PUT /variants/{id}` | Replaces `size` and `color` |
| `PATCH /variants/{id}/stock` | Adds `{"delta"}` to the stock. Answers 409 if the stock would go negative |
| `DELETE /variants/{id}` | Deletes one |

Prices are in whole rupiah and belong to the product. Stock belongs to each variant. A description holds at most 5000 characters. Migrations in `migrations/` run when the service starts.

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
| `GET /.well-known/jwks.json` | Returns the public key that checks tokens |
| `GET /internal/users/{id}` | Returns one user, for other services. It needs `Authorization: Bearer <INTERNAL_TOKEN>`, and web has no route to it |

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
| `POST /orders` | Creates an order from `{"items": [{"variant_id", "quantity"}]}`, with 1 to 20 different variants and a quantity of 1 to 20 each. Answers 409 if the user already has 5 open orders, and 503 while catalog-service is down |
| `GET /orders?limit=20&offset=0` | Lists the user's orders, newest first |
| `GET /orders/{id}` | Returns one |
| `POST /orders/{id}/pay` | Asks for a simulated payment with `{"simulate": "success"}` or `{"simulate": "failure"}`. Answers 202, or 409 if the order isn't waiting for payment |
| `POST /orders/{id}/cancel` | Cancels an order that is waiting for payment and returns its stock. Answers 409 in any other status |

Prices and names are copied from catalog-service when the order is created, so later catalog changes don't touch it.

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
| `order.cancelled` | `order.events` | `{"order_id", "user_id", "reason", "total", "items"}` |
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
| `GET`, `POST /products`, `GET`, `PUT`, `DELETE /products/{id}`, `POST /products/{id}/variants` | catalog-service's product routes |
| `PUT`, `DELETE /variants/{id}`, `PATCH /variants/{id}/stock` | catalog-service's variant and stock routes |

catalog-service checks the changes itself, and admin-service passes its answers on, except that a refused internal token answers 502, so it can't look like a logged-out admin. Every change is logged with the admin's id.

It needs `USER_URL`, `ORDER_URL`, `CATALOG_URL`, `USER_INTERNAL_TOKEN`, `ORDER_INTERNAL_TOKEN` and `CATALOG_INTERNAL_TOKEN`. The tests use fake services:

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

The *Produk* page (`/admin/#produk`) manages categories, products with their price, category and description, and each product's variants. Stock changes by an amount added or taken away, not by a new total, so orders placed meanwhile aren't overwritten. Deleting a product or variant asks first, and old orders keep their own copy of names and prices.

The *Pesanan* page lists orders by step: *Perlu dikemas* (paid), *Dikemas*, *Dikirim*, *Selesai* and *Semua*. Each order offers only its next step, and shipping asks for the tracking number. It needs `ADMIN_URL`.

## realtime-service

| Request | Does |
|---|---|
| `GET /ws` | Opens a WebSocket for the user in the `Authorization: Bearer <token>` header. Answers 401 without a valid token, 429 above 5 connections per user, and 503 above 500 in total |

It subscribes to `orders.*` on NATS and sends each signal only to its owner's connections, as `{"order_id", "status"}`. It has no database, and a signal only says that something changed, so the browser reads the order from order-service.

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
| `POST /api/register`, `POST /api/login`, `GET /api/me` | user-service |
| `POST /api/logout` | Handled here. It only clears the cookie |
| `GET /api/events` | realtime-service, as a WebSocket with the token from the cookie. Answers 403 if `Origin` isn't this site |
| `POST /api/orders`, `GET /api/orders`, `GET /api/orders/{id}`, `POST /api/orders/{id}/pay` | order-service, with the token from the cookie |
| `GET` and `DELETE /api/cart`, `POST /api/cart/merge`, `PUT` and `DELETE /api/cart/items/{id}` | cart-service, with the token from the cookie |

Anything else answers 404, so the other services' write endpoints stay inside the cluster, and a new endpoint stays private until it's added here.

Login keeps the token in an `HttpOnly`, `Secure`, `SameSite=Lax` cookie, so page scripts can't read it. Because of `Secure`, browsers only keep it on HTTPS and `localhost`. `POST` requests must be `application/json`, which a form on another site can't send without asking. A WebSocket handshake carries the cookie without asking, so `/api/events` checks `Origin` instead.

A guest's cart lives in the browser's `localStorage`, so it needs no account. A customer's lives in cart-service, so it follows them to any browser and leaves the browser when they log out. Logging in moves a guest's cart to the account. The browser's copy stays until the account has it. Either way the cart holds only ids and quantities, and reads names, prices and stock again each time it opens. Checkout sends one `POST /api/orders`, which holds at most 20 different variants, up to 20 of each. An order is all or nothing: if one item is short, the whole order is rejected, and the customer can put it back in the cart.

Products have no photos yet, so the pages draw a garment for the categories Dress, Blouse, Rok and Hijab, and show the first letter of the name for anything else. The heading font is Cormorant Garamond, served from `static/fonts/` under its [license](ecommerce/web/static/fonts/OFL.txt), because the Content-Security-Policy only allows this site's own files.

It needs `CATALOG_URL`, `SEARCH_URL`, `USER_URL`, `ORDER_URL`, `REALTIME_URL` and `CART_URL`, and won't start without them.

The tests use fake services, so they need nothing running:

```bash
cd ecommerce/web
go test -race ./...
```
