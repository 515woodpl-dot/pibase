// Email builder for datbase.pb.js (loaded with require; PocketBase runs each hook handler in isolation).
module.exports = { build: function (e, action, subject, intro, button) {
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => "&#" + c.charCodeAt(0) + ";");
  const name = (e.app.settings().meta.appName || "the app").replace(/[\r\n<>"]/g, "").slice(0, 60);
  const url = (e.app.store().get("datbase_mail_cfg") || {}).publicUrl + "/" + encodeURIComponent(e.record.collection().name) + "/" + action + "?token=" + encodeURIComponent(e.meta.token || "");
  e.message.subject = subject.replace("{APP}", name);
  e.message.html =
    '<div style="font-family:system-ui,sans-serif;max-width:520px;margin:auto;color:#111">' +
    "<p>" + esc(intro.replace("{APP}", name)) + "</p>" +
    '<p><a href="' + esc(url) + '" style="display:inline-block;background:#2d7daf;color:#fff;padding:10px 16px;border-radius:6px;text-decoration:none">' + esc(button) + "</a></p>" +
    '<p style="font-size:12px;color:#666">Or open: ' + esc(url) + "</p>" +
    '<p style="font-size:12px;color:#666">If you didn\'t ask for this, ignore this email.</p></div>';
  e.message.text = intro.replace("{APP}", name) + "\n\n" + button + ": " + url + "\n\nIf you didn't ask for this, ignore this email.";
  // The headers map may be nil on the Go side: always assign a fresh object.
  const h = {};
  const old = e.message.headers;
  if (old) for (const k in old) h[k] = old[k];
  h["X-Datbase-Mail"] = "1";
  e.message.headers = h;
} };
