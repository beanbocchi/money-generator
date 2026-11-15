# Telegram approval buttons for updates the fuse holds. Sourced by update.sh, which
# provides log, STATE_DIR and TELEGRAM_API.

# Approve / Keep-old buttons for a held update. This must be a SEPARATE bot from
# freqtrade's: Telegram allows one getUpdates consumer per token and freqtrade already
# polls its own, so sharing it fails with 409 Conflict and breaks both. It must also have
# no webhook set (Telegram then refuses getUpdates and delivers the presses elsewhere).
#
# The buttons go to APPROVAL_CHAT_ID (default: freqtrade's chat, which is a group). In a
# group anyone present can press Approve, so a press only counts when the presser's user
# id is in APPROVER_IDS. For a private chat that defaults to the chat id itself; for a
# group it must be set, and the buttons stay off until it is.
APPROVAL_TELEGRAM_TOKEN="${APPROVAL_TELEGRAM_TOKEN:-}"
APPROVAL_CHAT_ID="${APPROVAL_CHAT_ID:-${FREQTRADE__TELEGRAM__CHAT_ID:-}}"
APPROVER_IDS="${APPROVER_IDS:-}"
if [ -z "$APPROVER_IDS" ] && [[ "$APPROVAL_CHAT_ID" =~ ^[0-9]+$ ]]; then APPROVER_IDS=$APPROVAL_CHAT_ID; fi
APPROVER_IDS=" ${APPROVER_IDS//,/ } "     # padded, for an exact " id " match

approval_enabled() { [ -n "$APPROVAL_TELEGRAM_TOKEN" ] && [ -n "$APPROVAL_CHAT_ID" ] && [ -n "${APPROVER_IDS// /}" ]; }
is_approver() { [[ "$APPROVER_IDS" == *" $1 "* ]]; }

# One call to the approval bot: $1 method, then name=value pairs. Prints the JSON reply.
tg() {
  local method=$1 max=${TG_MAX_TIME:-20}; shift
  local args=() kv
  for kv in "$@"; do args+=(--data-urlencode "$kv"); done
  curl -sf -m "$max" -X POST "$TELEGRAM_API/bot${APPROVAL_TELEGRAM_TOKEN}/$method" "${args[@]}"
}

# $1 digest, $2 message text. Returns non-zero if it did not go out.
send_approval() {
  local markup
  markup=$(jq -nc --arg d "$1" \
    '{inline_keyboard: [[{text: "✅ Approve", callback_data: ("approve:" + $d)},
                         {text: "❌ Keep old", callback_data: ("reject:" + $d)}]]}')
  tg sendMessage "chat_id=$APPROVAL_CHAT_ID" "text=$2" "reply_markup=$markup" >/dev/null && return 0
  log "approval message failed to send"
  return 1
}

# Long-polls the approval bot until epoch $1. Returns 0 as soon as an approval lands, so
# the caller can run a cycle at once instead of waiting out the rest of the interval.
listen() {
  local deadline=$1 off now t resp u id cq from chat data cqid msgid pending verdict
  off=$(cat "$STATE_DIR/tg_offset" 2>/dev/null || echo 0)
  while now=$(date -u +%s); [ "$now" -lt "$deadline" ]; do
    t=$((deadline - now)); [ "$t" -gt 50 ] && t=50
    if ! resp=$(TG_MAX_TIME=$((t + 15)) tg getUpdates "offset=$off" "timeout=$t" \
                'allowed_updates=["callback_query"]'); then
      sleep 5; continue            # Telegram unreachable: keep the deadline, retry
    fi
    while IFS= read -r u; do
      id=$(jq -r '.update_id' <<<"$u")
      off=$((id + 1)); echo "$off" > "$STATE_DIR/tg_offset"
      cq=$(jq -c '.callback_query // empty' <<<"$u"); [ -n "$cq" ] || continue
      from=$(jq -r '.from.id' <<<"$cq"); chat=$(jq -r '.message.chat.id // empty' <<<"$cq")
      data=$(jq -r '.data // empty' <<<"$cq"); cqid=$(jq -r '.id' <<<"$cq")
      msgid=$(jq -r '.message.message_id // empty' <<<"$cq")
      if [ "$chat" != "$APPROVAL_CHAT_ID" ] || ! is_approver "$from"; then
        log "ignored a button press from user $from in chat $chat (not an approver here)"
        tg answerCallbackQuery "callback_query_id=$cqid" "text=Not authorised" >/dev/null
        continue
      fi
      pending=$(head -1 "$STATE_DIR/pending_risk" 2>/dev/null | grep -oE 'digest [0-9a-f]+' | cut -d' ' -f2)
      case "$data" in
        approve:*|reject:*) ;;
        *) tg answerCallbackQuery "callback_query_id=$cqid" >/dev/null; continue ;;
      esac
      if [ -z "$pending" ] || [ "${data#*:}" != "$pending" ]; then
        log "ignored a stale button for ${data#*:} (pending: ${pending:-none})"
        tg answerCallbackQuery "callback_query_id=$cqid" "text=Outdated - this change is no longer pending" >/dev/null
        [ -n "$msgid" ] && tg editMessageReplyMarkup "chat_id=$chat" "message_id=$msgid" \
          'reply_markup={"inline_keyboard":[]}' >/dev/null
        continue
      fi
      if [ "${data%%:*}" = "approve" ]; then
        echo "$pending" > "$STATE_DIR/approved_risk"
        verdict="✅ Approved - deploying on this check (restart cap and volatility gate still apply)"
        log "approved $pending via Telegram"
      else
        echo "$pending" > "$STATE_DIR/rejected_risk"
        verdict="❌ Kept the running version. Approve later with bin/approve-update.sh"
        log "kept old version for $pending via Telegram"
      fi
      tg answerCallbackQuery "callback_query_id=$cqid" "text=${verdict%% -*}" >/dev/null
      [ -n "$msgid" ] && tg editMessageReplyMarkup "chat_id=$chat" "message_id=$msgid" \
        'reply_markup={"inline_keyboard":[]}' >/dev/null
      tg sendMessage "chat_id=$chat" "text=$verdict" >/dev/null
      [ "${data%%:*}" = "approve" ] && return 0
    done < <(jq -c '.result[]?' <<<"$resp" 2>/dev/null)
  done
  return 1
}

# Setup check, run by hand with APPROVAL_SELFTEST=1. Sends a button to APPROVAL_CHAT_ID and
# waits for a press. With APPROVER_IDS unset it prints who pressed, so you can copy your
# user id into .env; with it set, it confirms that an approver's press reaches the updater.
selftest() {
  local resp off=0 u from who deadline=$(( $(date -u +%s) + ${SELFTEST_SECONDS:-180} ))
  [ -n "$APPROVAL_TELEGRAM_TOKEN" ] || { log "selftest: APPROVAL_TELEGRAM_TOKEN is empty"; return 1; }
  [ -n "$APPROVAL_CHAT_ID" ] || { log "selftest: APPROVAL_CHAT_ID is empty"; return 1; }
  resp=$(tg getMe) || { log "selftest: getMe failed - wrong token or Telegram unreachable"; return 1; }
  log "selftest: bot is @$(jq -r '.result.username' <<<"$resp")"
  resp=$(tg getWebhookInfo)
  if [ -n "$(jq -r '.result.url // empty' <<<"$resp")" ]; then
    log "selftest: FAILED - this bot has a webhook, so Telegram sends its button presses to that URL instead. Use a bot of its own."
    return 1
  fi
  tg sendMessage "chat_id=$APPROVAL_CHAT_ID" "text=Updater approval test: press the button." \
    'reply_markup={"inline_keyboard":[[{"text":"✅ Test","callback_data":"selftest"}]]}' >/dev/null \
    || { log "selftest: could not post in $APPROVAL_CHAT_ID - is the bot in that chat?"; return 1; }
  log "selftest: test button posted, press it (${SELFTEST_SECONDS:-180}s)..."
  while [ "$(date -u +%s)" -lt "$deadline" ]; do
    resp=$(TG_MAX_TIME=40 tg getUpdates "offset=$off" "timeout=25" 'allowed_updates=["callback_query"]') || { sleep 3; continue; }
    while IFS= read -r u; do
      off=$(( $(jq -r '.update_id' <<<"$u") + 1 ))
      [ "$(jq -r '.callback_query.data // empty' <<<"$u")" = "selftest" ] || continue
      from=$(jq -r '.callback_query.from.id' <<<"$u")
      who=$(jq -r '.callback_query.from | "\(.first_name) @\(.username // "-")"' <<<"$u")
      tg answerCallbackQuery "callback_query_id=$(jq -r '.callback_query.id' <<<"$u")" "text=Received" >/dev/null
      tg getUpdates "offset=$off" "timeout=0" >/dev/null
      if [ -z "${APPROVER_IDS// /}" ]; then
        log "selftest: pressed by $who, user id $from. If that is you: set APPROVER_IDS=$from in .env"
        return 0
      elif is_approver "$from"; then
        log "selftest: PASSED - $who ($from) is an approver and the press reached the updater"; return 0
      else
        log "selftest: pressed by $who ($from), who is NOT in APPROVER_IDS - waiting for an approver"
      fi
    done < <(jq -c '.result[]?' <<<"$resp")
  done
  log "selftest: no approver press arrived"; return 1
}
