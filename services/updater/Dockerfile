FROM docker:cli
# bash for the script, git for the pull, curl for Telegram, jq to read Telegram updates
# (the approval buttons parse callback queries, and grep-parsing JSON that contains
# user-typed text is not a safe way to decide whether to deploy to a real-money bot)
RUN apk add --no-cache bash git curl jq
COPY update.sh approval.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/update.sh
ENTRYPOINT ["/usr/local/bin/update.sh"]
