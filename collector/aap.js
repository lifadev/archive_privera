/* @preserve aap.js v0.1.0 - https://privera.io */

(async function (doc) {
  function clean(url, strict) {
    url = new URL(url);
    const min = `${url.protocol}//${url.host}`;
    return strict ? min : `${min}${url.pathname}`;
  }
  function attr(attrs, key) {
    return attrs && (attrs.getNamedItem(`pe-${key}`) || {}).value;
  }

  const attrs = (doc.currentScript || {}).attributes;
  let endpoint = attr(attrs, "endpoint");
  if (!endpoint) {
    return console.error("privera: 'endpoint' is missing.");
  }

  const canonical = doc.querySelector("link[rel='canonical']") || {};
  const payload = new URLSearchParams({
    type: "page",
    title: doc.title,
    url: clean(canonical.href || doc.location.href),
  });
  doc.referrer && payload.set("referrer", clean(doc.referrer), true);

  await fetch(endpoint, {
    method: "POST",
    headers: { "content-type": "text/plain; charset=UTF-8" },
    referrerPolicy: "no-referrer",
    body: payload.toString(),
  });
})(document);
