FROM python:3.15.0rc2-alpine@sha256:c847d755a8927c714eec064075b1f10549730b3233c758c51a403e60b53b9100

RUN pip install --no-cache-dir linkchecker

COPY linkcheckerrc /etc/linkchecker/linkcheckerrc

ENTRYPOINT ["linkchecker", "--config=/etc/linkchecker/linkcheckerrc"]
