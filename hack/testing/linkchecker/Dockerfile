FROM python:3.15.0rc3-alpine@sha256:f288a00331ddad8ddf86aa9d83331f188d9a5b1999dea3762adf2af1968b0c04

RUN pip install --no-cache-dir linkchecker

COPY linkcheckerrc /etc/linkchecker/linkcheckerrc

ENTRYPOINT ["linkchecker", "--config=/etc/linkchecker/linkcheckerrc"]
