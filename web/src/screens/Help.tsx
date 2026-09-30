// Help (docs/HELP.md §3): the guide list by section, a search box that
// takes plain questions, and each guide's page. Signed in, it sits in the
// app's frame and shows what the person's role may read; signed out, it's
// a page of its own with only the sign-in guides. The server decides what
// each caller gets; this page only shows it. Guides arrive as blocks and
// are drawn here as elements, never as HTML.
import { useEffect, useRef, useState, type FormEvent, type MouseEvent, type ReactNode } from "react";
import { ArrowLeft, FileText, Search } from "lucide-react";
import { api } from "@/api/client";
import { Wordmark } from "@/components/brand";
import { navigate } from "@/hooks/useRoute";
import {
  guideInPath, guidePath, HELP_PATH, HELP_SECTIONS, loadGuides,
  type HelpBlock, type HelpGuide, type HelpGuideSummary, type HelpInline, type HelpSearchResult,
} from "@/lib/help";

export default function HelpScreen({ path, signedIn }: { path: string; signedIn: boolean }) {
  const name = guideInPath(path);
  const page = name ? <GuidePage key={name} name={name} /> : <HelpHome signedIn={signedIn} />;
  if (signedIn) return <div className="w-full max-w-3xl px-4 py-6 md:px-6">{page}</div>;
  return (
    <div className="min-h-dvh bg-background">
      <header className="flex h-16 items-center justify-between gap-3 border-b px-4 md:px-6">
        <Wordmark className="text-3xl" />
        <Link href="/">Back to sign in</Link>
      </header>
      <main className="mx-auto w-full max-w-3xl px-4 py-6 md:px-6">{page}</main>
    </div>
  );
}

/** A link inside the app: the address shows on hover, a click stays on the page. */
function Link({ href, children, className = "" }: { href: string; children: ReactNode; className?: string }) {
  const go = (e: MouseEvent) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    const [to, hash] = href.split("#");
    if (to === window.location.pathname && hash) {
      // A heading on this page: just go to it.
      window.history.replaceState(null, "", href);
      document.getElementById(hash)?.scrollIntoView();
      return;
    }
    navigate(href);
  };
  return <a href={href} onClick={go} className={`text-link underline-offset-4 hover:underline ${className}`}>{children}</a>;
}

function HelpHome({ signedIn }: { signedIn: boolean }) {
  const [guides, setGuides] = useState<HelpGuideSummary[] | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let live = true;
    void loadGuides().then((g) => {
      if (!live) return;
      setGuides(g);
      setFailed(g.length === 0);
    });
    return () => { live = false; };
  }, []);

  return (
    <>
      <h1 className="font-display text-3xl font-semibold tracking-tight">{signedIn ? "Help" : "Help signing in"}</h1>
      <p className="mt-1 text-sm text-muted-foreground">
        {signedIn ? "How-to guides for this Linx. Ask a question, or pick a guide below."
          : "Guides for signing in. Everything else is here once you've signed in."}
      </p>
      <HelpSearch />
      {failed && <p role="status" className="mt-8 text-sm">Help isn't available right now. Try again in a moment.</p>}
      {guides && guides.length > 0 && (
        <div className="mt-8 flex flex-col gap-8">
          {HELP_SECTIONS.map((s) => {
            const inSection = guides.filter((g) => g.section === s.id);
            if (inSection.length === 0) return null;
            return (
              <section key={s.id} aria-labelledby={`help-${s.id}`}>
                <h2 id={`help-${s.id}`} className="text-sm font-medium tracking-wide text-muted-foreground">{s.title.toUpperCase()}</h2>
                <ul className="mt-2 grid gap-x-6 sm:grid-cols-2">
                  {inSection.map((g) => (
                    <li key={g.name} className="border-b py-2.5">
                      <Link href={guidePath(g.name)} className="flex items-start gap-2 text-sm">
                        <FileText aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                        <span className="min-w-0 [overflow-wrap:anywhere]">{g.title}</span>
                      </Link>
                    </li>
                  ))}
                </ul>
              </section>
            );
          })}
        </div>
      )}
    </>
  );
}

// Searching as the person types, after a short pause, so a slow link
// isn't asked for every letter.
const SEARCH_PAUSE_MS = 350;

function HelpSearch() {
  const [q, setQ] = useState("");
  const [results, setResults] = useState<{ q: string; results: HelpSearchResult[] } | null>(null);
  const asked = useRef("");

  const search = async (question: string) => {
    const text = question.trim();
    asked.current = text;
    if (!text) {
      setResults(null);
      return;
    }
    const { data } = await api.GET("/api/v1/help/search", { params: { query: { q: text.slice(0, 500) } } });
    if (asked.current === text) setResults({ q: text, results: data?.results ?? [] });
  };

  useEffect(() => {
    const t = setTimeout(() => void search(q), SEARCH_PAUSE_MS);
    return () => clearTimeout(t);
  }, [q]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void search(q);
  };

  return (
    <div className="mt-6">
      <form role="search" onSubmit={submit} className="relative">
        <Search aria-hidden="true" className="pointer-events-none absolute inset-y-0 start-3 my-auto size-4 text-muted-foreground" />
        <input value={q} onChange={(e) => setQ(e.target.value)} maxLength={500} type="search"
          placeholder="Ask a question, like “how do I add a desk phone?”" aria-label="Search the guides"
          className="h-11 w-full rounded-md border bg-card ps-9 pe-3 text-sm outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50" />
      </form>
      {results && (
        <section aria-label="Search results" aria-live="polite" className="mt-4">
          {results.results.length === 0 ? (
            <p className="rounded-lg border bg-card p-4 text-sm">
              Nothing in the guides matches “{results.q}”. Try other words, or pick a guide below.
            </p>
          ) : (
            <ul className="flex flex-col gap-2">
              {results.results.map((r) => (
                <li key={`${r.guide}#${r.anchor ?? ""}`} className="rounded-lg border bg-card p-4">
                  <Link href={guidePath(r.guide, r.anchor)} className="text-sm font-medium [overflow-wrap:anywhere]">
                    {r.title}{r.heading ? ` › ${r.heading}` : ""}
                  </Link>
                  {r.lines.map((l, i) => (
                    <p key={i} className="mt-1 text-sm text-muted-foreground [overflow-wrap:anywhere]">{l}</p>
                  ))}
                </li>
              ))}
            </ul>
          )}
        </section>
      )}
    </div>
  );
}

function GuidePage({ name }: { name: string }) {
  const [guide, setGuide] = useState<HelpGuide | null | "missing">(null);
  useEffect(() => {
    let live = true;
    void api.GET("/api/v1/help/guides/{name}", { params: { path: { name } } }).then(({ data }) => {
      if (live) setGuide(data ?? "missing");
    });
    return () => { live = false; };
  }, [name]);

  // Open at the top, or at the heading the address names, once drawn.
  const top = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!guide) return;
    const id = decodeURIComponent(window.location.hash.slice(1));
    (id && document.getElementById(id) || top.current)?.scrollIntoView();
  }, [guide]);

  const topMark = <div ref={top} className="scroll-mt-6" />;
  const allGuides = <Link href={HELP_PATH} className="inline-flex items-center gap-1.5 text-sm"><ArrowLeft aria-hidden="true" className="size-4" />All guides</Link>;
  if (guide === null) return <>{topMark}{allGuides}<div className="mt-6" aria-busy="true" /></>;
  if (guide === "missing") {
    return (
      <>
        {topMark}{allGuides}
        <h1 className="mt-4 font-display text-3xl font-semibold tracking-tight">Guide not found</h1>
        <p className="mt-2 text-sm">There's no such guide here. Pick one from the list, or search for it.</p>
      </>
    );
  }
  return (
    <>
      {topMark}{allGuides}
      <article className="mt-4 text-[0.9375rem] leading-relaxed [overflow-wrap:anywhere]">
        {guide.blocks.map((b, i) => <GuideBlock key={i} block={b} guide={guide.name} />)}
      </article>
    </>
  );
}

function GuideBlock({ block: b, guide }: { block: HelpBlock; guide: string }) {
  switch (b.type) {
    case "heading":
      if (b.level === 1) return <h1 id={b.anchor} className="font-display text-3xl font-semibold tracking-tight">{b.text}</h1>;
      if (b.level === 2) return <h2 id={b.anchor} className="mt-8 scroll-mt-4 font-display text-xl font-semibold">{b.text}</h2>;
      return <h3 id={b.anchor} className="mt-6 scroll-mt-4 font-semibold">{b.text}</h3>;
    case "paragraph":
      return <p className="mt-3"><Inlines inlines={b.inlines ?? []} guide={guide} /></p>;
    case "list":
      return <GuideList block={b} guide={guide} />;
    case "code":
      return <pre className="mt-3 whitespace-pre-wrap rounded-md border bg-card px-3 py-2 font-mono text-sm [overflow-wrap:anywhere]">{b.text}</pre>;
    case "picture":
      return (
        <figure className="mt-4">
          <picture>
            <source srcSet={`/api/v1/help/pictures/dark-${b.picture}.webp`} media="(prefers-color-scheme: dark)" />
            <img src={`/api/v1/help/pictures/light-${b.picture}.webp`} alt={b.alt ?? ""} width={1440} height={900}
              loading="lazy" decoding="async" className="h-auto w-full rounded-md border" />
          </picture>
        </figure>
      );
  }
  return null;
}

function GuideList({ block, guide, nested = false }: { block: HelpBlock; guide: string; nested?: boolean }) {
  const items = (block.items ?? []).map((it, i) => (
    <li key={i} className="mt-1.5 ps-1">
      <Inlines inlines={it.inlines} guide={guide} />
      {it.list && <GuideList block={it.list} guide={guide} nested />}
    </li>
  ));
  const cls = `${nested ? "mt-1.5" : "mt-3"} ps-5`;
  return block.ordered
    ? <ol start={block.start} className={`${cls} list-decimal`}>{items}</ol>
    : <ul className={`${cls} list-disc`}>{items}</ul>;
}

function Inlines({ inlines, guide }: { inlines: HelpInline[]; guide: string }) {
  return (
    <>
      {inlines.map((x, i) => {
        switch (x.type) {
          case "bold":
            return <strong key={i} className="font-semibold">{x.text}</strong>;
          case "italic":
            return <em key={i}>{x.text}</em>;
          case "code":
            return <code key={i} className="rounded-sm border bg-card px-1 font-mono text-[0.875em]">{x.text}</code>;
          case "link":
            return <Link key={i} href={guidePath(x.guide || guide, x.anchor)}>{x.text}</Link>;
          default:
            return <span key={i}>{x.text}</span>;
        }
      })}
    </>
  );
}
