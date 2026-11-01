// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Group, Pagination as MantinePagination, type PaginationProps } from '@mantine/core';
import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';

/** Pages of a list, with names for its buttons (screen readers announce "Next page", "Page 3"). */
export function Pagination(props: PaginationProps) {
  const { t } = useTranslation();
  return (
    <MantinePagination
      getControlProps={(control) => ({ 'aria-label': t(`common.pagination.${control}`) })}
      getItemProps={(page) => ({ 'aria-label': t('common.pagination.page', { page }) })}
      {...props}
    />
  );
}

/**
 * The pages of a list of total items, size on each page, centred below it; nothing while one page holds them all.
 * When the list shrinks below the page shown (its last items were deleted), it turns to the new last page.
 */
export function ListPages({
  total,
  size,
  page,
  onChange,
}: {
  total: number;
  size: number;
  page: number;
  onChange: (page: number) => void;
}) {
  const pages = Math.ceil(total / size);
  useEffect(() => {
    if (pages > 0 && page > pages) onChange(pages);
  }, [page, pages, onChange]);
  if (pages <= 1) return null;
  return (
    <Group justify="center" mt="md">
      <Pagination total={pages} value={Math.min(page, pages)} onChange={onChange} />
    </Group>
  );
}
