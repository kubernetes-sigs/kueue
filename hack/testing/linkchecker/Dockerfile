FROM python:3.15.0rc2-alpine@sha256:b5c650ee9800c70fff92df975561d5398d9addb50df70ad0a0f9c9a2b62dd46a

RUN pip install --no-cache-dir linkchecker

COPY linkcheckerrc /etc/linkchecker/linkcheckerrc

ENTRYPOINT ["linkchecker", "--config=/etc/linkchecker/linkcheckerrc"]
