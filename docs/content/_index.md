---
title: "TamarackDB"
description: "An open source event store in Go, compliant with the DCB specification, served over HTTP, with its data in one SQLite file."
layout: hextra-home
---

{{< hextra/hero-headline >}}
  An event store for&nbsp;<br class="hx:sm:block hx:hidden" />Dynamic Consistency Boundaries
{{< /hextra/hero-headline >}}

{{< hextra/hero-subtitle >}}
  Open source, written in Go. An HTTP API, transactions, and one SQLite file.
{{< /hextra/hero-subtitle >}}

<div class="hx:mt-6 hx:mb-6">
{{< hextra/hero-button text="Get Started" link="/docs/quickstart/" >}}
</div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card title="DCB compliant" subtitle="Follows the DCB specification, with optimistic concurrency on writes." link="/docs/development/concepts/" >}}
  {{< hextra/feature-card title="HTTP API" subtitle="Send plain JSON requests from any language or platform." link="/docs/development/http-api/" >}}
  {{< hextra/feature-card title="Transactions" subtitle="A command's events and projections are written together, or not at all." link="/docs/development/concepts/#transactions" >}}
  {{< hextra/feature-card title="Simple deployment" subtitle="Static Linux binaries with no runtime and no external service to install." link="/docs/operations/install/" >}}
  {{< hextra/feature-card title="SQLite storage" subtitle="Events and projections live in one SQLite file, easy to inspect." link="/docs/key-features/" >}}
  {{< hextra/feature-card title="Built-in backup" subtitle="Keep an incremental copy of an instance's events." link="/docs/operations/backup/" >}}
{{< /hextra/feature-grid >}}
