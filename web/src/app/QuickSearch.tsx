// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Box,
  CloseButton,
  Group,
  Input,
  Kbd,
  Loader,
  Modal,
  ScrollArea,
  Text,
  Tooltip,
  VisuallyHidden,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import {
  IconFilePlus,
  IconFolderPlus,
  IconNews,
  IconPlus,
  IconSearch,
  IconShieldLock,
  IconUserCircle,
  IconWorldSearch,
} from '@tabler/icons-react';
import { keepPreviousData, useQuery } from '@tanstack/react-query';
import {
  createContext,
  use,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { api, unwrap } from '../api/client';
import { HitBadges, HitIcon, iconSpeaks, showsKey, type Hit } from '../components/SearchHit';
import { useCreateRepository } from '../pages/code/CreateRepo';
import { useCreateIssue } from '../pages/issues/CreateIssue';
import { useCreatePage } from '../pages/wiki/CreatePage';
import { productList, productOf, products, type ProductId } from '../products';
import { appHref } from '../showcase/preview';
import { useCodeProjectPlace, useIssueProjectPlace, useWikiPlace } from './places';
import {
  isApple,
  matchesWords,
  moveActive,
  opensInNewTab,
  opensQuickSearch,
  quickSearchKey,
} from './quickSearchKeys';
import { useSession } from './session';

const apple = typeof navigator !== 'undefined' && isApple(navigator.platform);

/** An issue key typed in full: Enter opens the issue even before the results are there. */
const issueKey = /^[A-Za-z][A-Za-z0-9_]{1,9}-[1-9][0-9]{0,9}$/;

/** The product a quick search looks in (the one on screen), until the person removes it. */
export interface QuickSearchScope {
  product: ProductId;
  /** The field's text within the product ("Search issues or type a key"). */
  placeholder: string;
  /** The address of the product's own search for a text (the issue navigator, the wiki search …). */
  go: (q: string) => string;
}

export interface OpenQuickSearch {
  /** Where to look; the scope of the page on screen when left out, all products with null. */
  scope?: QuickSearchScope | null;
  /** What is typed already (typing on the header's search field opens the quick search with it). */
  query?: string;
}

interface QuickSearchApi {
  open: (options?: OpenQuickSearch) => void;
  /** The scope of the product on screen, kept by the product's frame (useQuickSearchScope). */
  scopeRef: { current: QuickSearchScope | null };
}

const QuickSearchContext = createContext<QuickSearchApi>({ open: () => {}, scopeRef: { current: null } });

/** Opens the quick search. */
export function useQuickSearch() {
  return use(QuickSearchContext).open;
}

/**
 * Makes the quick search look in a product while its pages are on screen (Ctrl+K and the header's field); null on
 * the platform's pages, where it looks everywhere.
 */
export function useQuickSearchScope(scope: QuickSearchScope | null) {
  const { scopeRef } = use(QuickSearchContext);
  useEffect(() => {
    scopeRef.current = scope;
    return () => {
      if (scopeRef.current === scope) scopeRef.current = null;
    };
  }, [scopeRef, scope]);
}

/**
 * The quick search: Ctrl+K (⌘K on Apple devices) or the search field of the header open it from
 * anywhere. While one types it finds issues (by key and summary), pages, blog posts, spaces, projects,
 * repositories, pull requests, pipelines and people; on a product's pages it looks in that product (a chip that
 * can be removed says so), elsewhere in all products. Before one types it shows what one opened last; and it
 * offers actions. An editor that uses Ctrl+K itself (for links) keeps it while it has the focus.
 */
export function QuickSearchProvider({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const [opened, setOpened] = useState(false);
  const [instance, setInstance] = useState<{ n: number } & OpenQuickSearch>({ n: 0 });
  const scopeRef = useRef<QuickSearchScope | null>(null);
  const open = useCallback((options: OpenQuickSearch = {}) => {
    setInstance((i) => ({
      n: i.n + 1,
      scope: options.scope === undefined ? scopeRef.current : options.scope,
      query: options.query,
    }));
    setOpened(true);
  }, []);
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (!opensQuickSearch(e, apple)) return;
      e.preventDefault();
      // The key opens it anew (with an empty field, in the product on screen) or closes it.
      setOpened((was) => !was);
      setInstance((i) => ({ n: i.n + 1, scope: scopeRef.current }));
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
  const api = useMemo(() => ({ open, scopeRef }), [open]);
  return (
    <QuickSearchContext value={api}>
      {children}
      <Modal.Root opened={opened} onClose={() => setOpened(false)} size="lg" yOffset="10vh">
        <Modal.Overlay />
        <Modal.Content aria-label={t('quickSearch.label')} className="kogge-qs">
          {opened && (
            <Palette
              key={instance.n}
              initialScope={instance.scope ?? null}
              initialQuery={instance.query ?? ''}
              onClose={() => setOpened(false)}
            />
          )}
        </Modal.Content>
      </Modal.Root>
    </QuickSearchContext>
  );
}

/** The header's search button of narrow screens, where the search field gives way (and a hint of the keys). */
export function QuickSearchButton({ label }: { label?: string }) {
  const { t } = useTranslation();
  const open = useQuickSearch();
  const name = label ?? t('quickSearch.open', { key: quickSearchKey(apple) });
  return (
    <Tooltip label={name} openDelay={300}>
      <ActionIcon
        variant="subtle"
        size="lg"
        className="kogge-header-icon"
        aria-label={name}
        onClick={() => open()}
      >
        <IconSearch size={20} />
      </ActionIcon>
    </Tooltip>
  );
}

/**
 * The header's search field: a button that looks like one and opens the quick search in the product on screen
 * (typing on it opens it with what is typed). It shows the keys that do the same.
 */
export function HeaderSearchField({ label }: { label: string }) {
  const open = useQuickSearch();
  const key = quickSearchKey(apple);
  return (
    <button
      type="button"
      className="kogge-header-search"
      aria-label={label}
      aria-keyshortcuts={apple ? 'Meta+K' : 'Control+K'}
      aria-haspopup="dialog"
      onClick={() => open()}
      onKeyDown={(e) => {
        // A letter typed on the focused field starts the search with it.
        if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey && e.key !== ' ') {
          e.preventDefault();
          open({ query: e.key });
        }
      }}
    >
      <IconSearch size={16} aria-hidden />
      <span className="kogge-header-search-label">{label}</span>
      <Kbd size="xs" className="kogge-header-search-key" aria-hidden>
        {key.replace('+', ' ')}
      </Kbd>
    </button>
  );
}

interface Option {
  id: string;
  /** A short text of the option (for its key in lists). */
  label: string;
  content: ReactNode;
  /** The address the option opens (hits); actions run instead. */
  href?: string;
  run?: () => void;
  /** A hit of what was typed before, shown until the results of what is typed now are there. */
  stale?: boolean;
}

interface Section {
  id: string;
  label: string;
  icon?: ReactNode;
  options: Option[];
}

/** How a hit looks in the list. */
function HitContent({ hit }: { hit: Hit }) {
  const { t } = useTranslation();
  return (
    <Group gap="sm" wrap="nowrap" miw={0}>
      <Box className="kogge-qs-icon" aria-hidden={!iconSpeaks(hit)}>
        <HitIcon hit={hit} />
      </Box>
      <Box miw={0} style={{ flex: 1 }}>
        <Text size="sm" truncate>
          <VisuallyHidden>{t(`quickSearch.kind.${hit.kind}`)}: </VisuallyHidden>
          {showsKey(hit) && (
            <>
              <Text span fw={650} size="sm">
                {hit.key}
              </Text>{' '}
            </>
          )}
          {hit.title}
        </Text>
        {(hit.context || hit.kind === 'user') && (
          <Text size="xs" c="dimmed" truncate>
            {hit.kind === 'user' ? hit.key : hit.context}
            {hit.archived ? ` · ${t('quickSearch.archived')}` : ''}
          </Text>
        )}
      </Box>
      <HitBadges hit={hit} />
    </Group>
  );
}

function ActionContent({ icon, label }: { icon: ReactNode; label: string }) {
  return (
    <Group gap="sm" wrap="nowrap">
      <Box className="kogge-qs-icon" aria-hidden>
        {icon}
      </Box>
      <Text size="sm" truncate>
        {label}
      </Text>
    </Group>
  );
}

function Palette({
  initialScope,
  initialQuery,
  onClose,
}: {
  initialScope: QuickSearchScope | null;
  initialQuery: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [scope, setScope] = useState(initialScope);
  const scopeName = scope ? products[scope.product].name : '';
  const inputRef = useRef<HTMLInputElement>(null);
  const { can } = useSession();
  const createIssue = useCreateIssue();
  const createPage = useCreatePage();
  const createRepository = useCreateRepository();
  const issueProject = useIssueProjectPlace();
  const { space: wikiSpace, pageId: wikiPage } = useWikiPlace();
  const codeProject = useCodeProjectPlace();
  const [query, setQuery] = useState(initialQuery);
  const text = query.trim();
  const [debounced] = useDebouncedValue(text, 120);
  const results = useQuery({
    queryKey: ['quick-search', debounced],
    queryFn: ({ signal }) =>
      unwrap(api.GET('/api/v1/quick-search', { params: { query: { q: debounced } }, signal })),
    enabled: debounced !== '',
    placeholderData: keepPreviousData,
    staleTime: 10_000,
  });
  const recent = useQuery({
    queryKey: ['recent-visits'],
    queryFn: () => unwrap(api.GET('/api/v1/me/recent', { params: { query: { limit: 8 } } })),
    staleTime: 0,
  });
  // The results belong to what is typed now (not to what was typed before).
  const current = text !== '' && debounced === text && !results.isPlaceholderData && results.isSuccess;

  const sections = useMemo<Section[]>(() => {
    const go = (href: string) => () => {
      onClose();
      navigate(href);
    };
    const act = (run: () => void) => () => {
      onClose();
      run();
    };
    const actions: (Option & { words: string })[] = [
      {
        id: 'create-issue',
        label: t('quickSearch.action.createIssue'),
        words: t('quickSearch.words.create'),
        content: <ActionContent icon={<IconPlus size={18} />} label={t('quickSearch.action.createIssue')} />,
        run: act(() => createIssue({ project: issueProject })),
      },
      {
        id: 'create-page',
        label: t('quickSearch.action.createPage'),
        words: t('quickSearch.words.create'),
        content: (
          <ActionContent icon={<IconFilePlus size={18} />} label={t('quickSearch.action.createPage')} />
        ),
        run: act(() => createPage({ kind: 'page', space: wikiSpace, pageId: wikiPage })),
      },
      {
        id: 'create-blog',
        label: t('quickSearch.action.createBlogPost'),
        words: t('quickSearch.words.create'),
        content: (
          <ActionContent icon={<IconNews size={18} />} label={t('quickSearch.action.createBlogPost')} />
        ),
        run: act(() => createPage({ kind: 'blog', space: wikiSpace, pageId: wikiPage })),
      },
      {
        id: 'create-repository',
        label: t('quickSearch.action.createRepository'),
        words: t('quickSearch.words.create'),
        content: (
          <ActionContent
            icon={<IconFolderPlus size={18} />}
            label={t('quickSearch.action.createRepository')}
          />
        ),
        run: act(() => createRepository({ project: codeProject })),
      },
      ...productList.map((p) => ({
        id: `go-${p.id}`,
        label: t('quickSearch.action.goTo', { name: p.name }),
        words: `${t('quickSearch.words.go')} ${t(`products.${p.id}.features`)}`,
        content: (
          <ActionContent icon={<p.Logo size={18} />} label={t('quickSearch.action.goTo', { name: p.name })} />
        ),
        href: p.home,
        run: go(p.home),
      })),
      {
        id: 'profile',
        label: t('quickSearch.action.profile'),
        words: t('quickSearch.words.profile'),
        content: (
          <ActionContent icon={<IconUserCircle size={18} />} label={t('quickSearch.action.profile')} />
        ),
        href: '/profile',
        run: go('/profile'),
      },
      ...(can('admin')
        ? [
            {
              id: 'administration',
              label: t('quickSearch.action.administration'),
              words: t('quickSearch.words.administration'),
              content: (
                <ActionContent
                  icon={<IconShieldLock size={18} />}
                  label={t('quickSearch.action.administration')}
                />
              ),
              href: '/admin',
              run: go('/admin'),
            },
          ]
        : []),
    ];
    const out: Section[] = [];
    const hitOption = (prefix: string, h: Hit, stale = false): Option => ({
      id: `${prefix}-${h.kind}-${h.id}`,
      label: `${h.key ?? ''} ${h.title}`.trim(),
      content: <HitContent hit={h} />,
      href: h.url,
      run: go(h.url),
      stale,
    });
    if (text === '') {
      const visited = (recent.data ?? []).filter((h) => !scope || productOf(h.url) === scope.product);
      if (visited.length > 0) {
        out.push({
          id: 'recent',
          label: t('quickSearch.recent'),
          options: visited.map((h) => hitOption('recent', h)),
        });
      }
      out.push({ id: 'actions', label: t('quickSearch.actions'), options: actions });
      return out;
    }
    const groups = results.data?.groups ?? [];
    for (const g of groups) {
      if (scope && g.product !== scope.product) continue;
      const product = g.product === 'people' ? null : products[g.product as ProductId];
      out.push({
        id: g.product,
        label: product ? product.name : t('quickSearch.people'),
        icon: product ? <product.Logo size={16} /> : <IconUserCircle size={16} />,
        options: g.hits.map((h) => hitOption(g.product, h, !current)),
      });
    }
    const matching = actions.filter((a) => matchesWords(`${a.label} ${a.words}`, text));
    if (matching.length > 0) out.push({ id: 'actions', label: t('quickSearch.actions'), options: matching });
    // Found elsewhere: one step widens the search to all products.
    const elsewhere = scope
      ? groups.filter((g) => g.product !== scope.product).reduce((n, g) => n + g.hits.length, 0)
      : 0;
    out.push({
      id: 'everything',
      label: t('quickSearch.everything'),
      options: [
        ...(scope
          ? [
              {
                id: 'search-product',
                label: t('quickSearch.searchProduct', { query: text, name: scopeName }),
                content: (
                  <ActionContent
                    icon={<IconSearch size={18} />}
                    label={t('quickSearch.searchProduct', { query: text, name: scopeName })}
                  />
                ),
                href: scope.go(text),
                run: go(scope.go(text)),
              },
            ]
          : []),
        ...(elsewhere > 0
          ? [
              {
                id: 'widen',
                label: t('quickSearch.widen', { count: elsewhere }),
                content: (
                  <ActionContent
                    icon={<IconWorldSearch size={18} />}
                    label={t('quickSearch.widen', { count: elsewhere })}
                  />
                ),
                // The field keeps the focus (options do not take it).
                run: () => setScope(null),
              },
            ]
          : []),
        {
          id: 'search-everything',
          label: t('quickSearch.searchEverything', { query: text }),
          content: (
            <ActionContent
              icon={<IconSearch size={18} />}
              label={t('quickSearch.searchEverything', { query: text })}
            />
          ),
          href: `/search?q=${encodeURIComponent(text)}`,
          run: go(`/search?q=${encodeURIComponent(text)}`),
        },
      ],
    });
    return out;
  }, [
    t,
    text,
    results.data,
    recent.data,
    can,
    navigate,
    onClose,
    createIssue,
    createPage,
    createRepository,
    issueProject,
    wikiSpace,
    wikiPage,
    codeProject,
    current,
    scope,
    scopeName,
  ]);

  const options = sections.flatMap((s) => s.options);
  const starts: number[] = [];
  sections.reduce((n, s) => {
    starts.push(n);
    return n + s.options.length;
  }, 0);
  // The active option belongs to a list: a new list starts at its first option, the best match (an exact key).
  const optionIds = options.map((o) => o.id).join('|');
  const [chosen, setChosen] = useState({ list: '', index: 0 });
  const active = chosen.list === optionIds ? chosen.index : 0;
  const setActive = (index: number) => setChosen({ list: optionIds, index });
  const listId = useId();
  const domId = (o: Option) => `${listId}-${o.id}`;
  const activeOption = options[Math.min(active, options.length - 1)];
  useEffect(() => {
    if (activeOption) document.getElementById(domId(activeOption))?.scrollIntoView({ block: 'nearest' });
  });

  const choose = (o: Option | undefined, newTab: boolean) => {
    if (!o) return;
    if (newTab && o.href) {
      window.open(appHref(o.href), '_blank', 'noopener');
      return;
    }
    o.run?.();
  };
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return;
    // Backspace in the empty field removes the product: the search widens to all products.
    if (
      e.key === 'Backspace' &&
      scope &&
      e.currentTarget.selectionStart === 0 &&
      e.currentTarget.selectionEnd === 0
    ) {
      e.preventDefault();
      setScope(null);
      return;
    }
    const next = moveActive(active, options.length, e.key, starts);
    if (next !== null) {
      e.preventDefault();
      setActive(next);
      return;
    }
    if (e.key === 'Enter') {
      e.preventDefault();
      // Before the results of what is typed are there, an issue key opens its issue, other texts the search
      // page (actions are there at once).
      if (text !== '' && (!activeOption || activeOption.stale)) {
        onClose();
        navigate(
          issueKey.test(text)
            ? `/browse/${text.toUpperCase()}`
            : scope
              ? scope.go(text)
              : `/search?q=${encodeURIComponent(text)}`,
        );
        return;
      }
      choose(activeOption, opensInNewTab(e, apple));
    }
  };

  const hits = (results.data?.groups ?? [])
    .filter((g) => !scope || g.product === scope.product)
    .reduce((n, g) => n + g.hits.length, 0);
  const missing = (results.data?.incomplete ?? []).map((p) =>
    p === 'people' ? t('quickSearch.people') : (products[p as ProductId]?.name ?? p),
  );
  let status = '';
  if (text !== '' && current)
    status = hits === 0 ? t('quickSearch.nothing') : t('quickSearch.count', { count: hits });
  return (
    <Box>
      <Group gap={8} wrap="nowrap" px="md" py={6} className="kogge-qs-input">
        <IconSearch size={18} className="kogge-qs-search-icon" aria-hidden />
        {scope && (
          <span className="kogge-qs-scope">
            {(() => {
              const P = products[scope.product];
              return <P.Logo size={16} />;
            })()}
            <span>{scopeName}</span>
            <CloseButton
              size="xs"
              aria-label={t('quickSearch.removeScope', { name: scopeName })}
              onClick={() => {
                setScope(null);
                inputRef.current?.focus();
              }}
            />
          </span>
        )}
        <Input
          ref={inputRef}
          size="md"
          variant="unstyled"
          style={{ flex: 1, minWidth: 0 }}
          placeholder={scope ? scope.placeholder : t('quickSearch.placeholder')}
          aria-label={t('quickSearch.label')}
          aria-description={scope ? t('quickSearch.scoped', { name: scopeName }) : undefined}
          role="combobox"
          aria-expanded={options.length > 0}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={activeOption ? domId(activeOption) : undefined}
          autoComplete="off"
          spellCheck={false}
          data-autofocus
          value={query}
          onChange={(e) => setQuery(e.currentTarget.value)}
          onKeyDown={onKeyDown}
        />
        {results.isFetching ? <Loader size="xs" /> : <Kbd size="xs">Esc</Kbd>}
      </Group>
      <ScrollArea.Autosize mah="min(60vh, 520px)" type="auto" className="kogge-qs-list">
        <div id={listId} role="listbox" aria-label={t('quickSearch.results')}>
          {sections.map((s) => {
            const headingId = `${listId}-${s.id}`;
            return (
              <div key={s.id} role="group" aria-labelledby={headingId}>
                <Group id={headingId} gap={6} className="kogge-qs-heading" role="presentation">
                  {s.icon}
                  <Text size="xs" fw={700} c="dimmed" tt="uppercase">
                    {s.label}
                  </Text>
                </Group>
                {s.options.map((o) => {
                  const index = options.indexOf(o);
                  return (
                    <div
                      key={o.id}
                      id={domId(o)}
                      role="option"
                      aria-selected={o === activeOption}
                      className="kogge-qs-option"
                      data-active={o === activeOption || undefined}
                      onMouseMove={() => index !== active && setActive(index)}
                      onMouseDown={(e) => e.preventDefault()}
                      onClick={(e) => choose(o, opensInNewTab(e, apple))}
                    >
                      {o.content}
                    </div>
                  );
                })}
              </div>
            );
          })}
        </div>
        {text !== '' && current && hits === 0 && (
          <Text size="sm" c="dimmed" px="md" py="xs">
            {t('quickSearch.nothing')}
          </Text>
        )}
        {missing.length > 0 && (
          <Text size="xs" c="dimmed" px="md" py="xs">
            {t('quickSearch.incomplete', { products: missing.join(', ') })}
          </Text>
        )}
      </ScrollArea.Autosize>
      <Group gap="md" px="md" py={8} className="kogge-qs-footer" aria-hidden>
        <Text size="xs" c="dimmed">
          <Kbd size="xs">↑</Kbd> <Kbd size="xs">↓</Kbd> {t('quickSearch.hint.move')}
        </Text>
        <Text size="xs" c="dimmed">
          <Kbd size="xs">↵</Kbd> {t('quickSearch.hint.open')}
        </Text>
        <Text size="xs" c="dimmed">
          <Kbd size="xs">{apple ? '⌘' : 'Ctrl'}</Kbd> <Kbd size="xs">↵</Kbd> {t('quickSearch.hint.newTab')}
        </Text>
        <Text size="xs" c="dimmed">
          <Kbd size="xs">Esc</Kbd> {t('quickSearch.hint.close')}
        </Text>
      </Group>
      <VisuallyHidden aria-live="polite">{status}</VisuallyHidden>
    </Box>
  );
}
