FROM python:3.15.2-alpine@sha256:0f9552bb28711333da16915ec146f5e6bb44a7fdac9ced15dc17c2e0206346dd

RUN pip install --no-cache-dir linkchecker

COPY linkcheckerrc /etc/linkchecker/linkcheckerrc

ENTRYPOINT ["linkchecker", "--config=/etc/linkchecker/linkcheckerrc"]
