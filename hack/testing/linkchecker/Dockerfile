FROM python:3.15.0rc1-alpine@sha256:c31ce768b814aa1cd3e9247f5d06f4713cf8e4140b707a1bf31fa18411c7c219

RUN pip install --no-cache-dir linkchecker

COPY linkcheckerrc /etc/linkchecker/linkcheckerrc

ENTRYPOINT ["linkchecker", "--config=/etc/linkchecker/linkcheckerrc"]
