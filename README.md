# AldiApps

Application source code, one folder per application. Each folder has its own `Dockerfile`. The Kubernetes manifests are in [`aldiCovo/AldiDevOps`](https://github.com/aldiCovo/AldiDevOps), in `apps/` under the same folder name.

| Folder | Application |
|---|---|
| [`cvaldi/`](cvaldi/) | Online CV (English / Indonesian), live at **https://aldilukito.aldiab.ai.id** |
| [`catalog-service/`](catalog-service/) | Go API for the shop's products and stock, using PostgreSQL. Only reachable inside the cluster |

## Deploy

Merge or push to `main`, then run this on a machine with Docker:

```bash
./build.sh cvaldi
```

It pushes `ghcr.io/aldicovo/cvaldi:<commit>`, using the last commit that changed the folder, and writes that tag to the manifests. ArgoCD then deploys it.

A new image is private on GHCR, and the cluster pulls without credentials. After an application's first build, set its package to public in the package settings on GitHub.

## catalog-service

| Request | Does |
|---|---|
| `GET /products?limit=20&offset=0` | Lists products |
| `POST /products` | Creates one from `{"name", "description", "price", "stock"}`. `name` and `price` are required |
| `GET /products/{id}` | Returns one |
| `PUT /products/{id}` | Replaces `name`, `description` and `price` |
| `PATCH /products/{id}/stock` | Adds `{"delta"}` to the stock. Answers 409 if the stock would go negative |
| `DELETE /products/{id}` | Deletes one |

Prices are in whole rupiah. Migrations in `migrations/` run when the service starts.

The tests need a throwaway PostgreSQL, because they empty the table:

```bash
cd catalog-service
TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:5432/catalog?sslmode=disable' go test -race ./...
```
