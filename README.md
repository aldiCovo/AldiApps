# AldiApps

Application source code, one folder per application. Each folder has its own `Dockerfile`. The Kubernetes manifests are in [`aldiCovo/AldiDevOps`](https://github.com/aldiCovo/AldiDevOps), in `apps/` under the same folder name.

| Folder | Application |
|---|---|
| [`cvaldi/`](cvaldi/) | Online CV (English / Indonesian), live at **https://aldilukito.aldiab.ai.id** |

## Deploy

Merge or push to `main`, then run this on a machine with Docker:

```bash
./build.sh cvaldi
```

It pushes `ghcr.io/aldicovo/cvaldi:<commit>`, using the last commit that changed the folder, and writes that tag to the manifests. ArgoCD then deploys it.
