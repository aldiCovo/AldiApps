# CV Aldi Lukito

Online CV (English / Indonesian), live at **https://aldilukito.aldiab.ai.id**

To deploy, commit and push to `main`, then run `./build.sh` on a machine with Docker. It pushes `ghcr.io/aldicovo/cv-aldi-lukito:<commit>` and writes that tag to the Kubernetes manifests in [`aldiCovo/AldiDevOps`](https://github.com/aldiCovo/AldiDevOps/tree/main/CV_Aldi_Lukito). ArgoCD then deploys it.
