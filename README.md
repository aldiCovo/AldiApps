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
| [`ecommerce/web/`](ecommerce/web/) | The shop's pages, and the only part the internet reaches, at **https://prastika.aldiab.ai.id** |

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

Prices are in whole rupiah and belong to the product. Stock belongs to each variant. Migrations in `migrations/` run when the service starts.

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

Prices and names are copied from catalog-service when the order is created, so later catalog changes don't touch it.

| Status | Means |
|---|---|
| `pending` | Waiting for catalog-service to reserve the stock |
| `awaiting_payment` | Stock is held until `expires_at`, which is `PAYMENT_WINDOW` (default `15m`) after the reservation |
| `paying` | Waiting for payment-service. It can no longer expire, so a slow payment-service can't cancel an order that is being paid |
| `paid` | Finished |
| `rejected` | Not enough stock. `reason` is `out_of_stock` |
| `cancelled` | Stock was returned. `reason` is `payment_failed` or `expired` |

### The order saga

No service reads another's database. Each step commits its change and its event in one transaction, using the same outbox as catalog-service:

1. order-service saves a `pending` order and sends `order.created`
2. catalog-service reserves every item or none, and sends `stock.reserved` or `stock.rejected`
3. order-service moves to `awaiting_payment` or `rejected`
4. `POST /orders/{id}/pay` moves to `paying` and sends `payment.requested`
5. payment-service records the result and sends `payment.succeeded` or `payment.failed`
6. order-service moves to `paid`. On a failure it moves to `cancelled` and sends `order.cancelled`, and catalog-service returns the stock

A loop checks every 5 seconds for orders past `expires_at`, cancels them and sends `order.cancelled`.

| Event | Exchange | `data` |
|---|---|---|
| `order.created` | `order.events` | `{"order_id", "items": [{"variant_id", "quantity"}]}` |
| `order.cancelled` | `order.events` | `{"order_id", "reason"}` |
| `payment.requested` | `order.events` | `{"order_id", "amount", "outcome"}` |

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

## web

Serves the pages in `static/`, and passes on only these requests:

| Request | Goes to |
|---|---|
| `GET /api/categories`, `GET /api/products/{id}` | catalog-service |
| `GET /api/search` | search-service |
| `POST /api/register`, `POST /api/login`, `GET /api/me` | user-service |
| `POST /api/logout` | Handled here. It only clears the cookie |
| `POST /api/orders`, `GET /api/orders`, `GET /api/orders/{id}`, `POST /api/orders/{id}/pay` | order-service, with the token from the cookie |

Anything else answers 404, so the other services' write endpoints stay inside the cluster, and a new endpoint stays private until it's added here.

Login keeps the token in an `HttpOnly`, `Secure`, `SameSite=Lax` cookie, so page scripts can't read it. Because of `Secure`, browsers only keep it on HTTPS and `localhost`. `POST` requests must be `application/json`, which a form on another site can't send without asking.

It needs `CATALOG_URL`, `SEARCH_URL`, `USER_URL` and `ORDER_URL`, and won't start without them.

The tests use fake services, so they need nothing running:

```bash
cd ecommerce/web
go test -race ./...
```
