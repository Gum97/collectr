import { useQuery } from '@tanstack/react-query'
import { api, type List } from './api'

export interface Project {
  id: string
  name: string
  slug: string
  default_retention_days: number | null
  archived_at: string | null
  member_count: number
  /** '' when the reader holds no grant on this project specifically. */
  my_role: string
  /** How the reader reaches it, decided by the API rather than inferred here. */
  access: 'org' | 'project' | 'none' 
  created_at: string
}

export function useProjects() {
  return useQuery({
    queryKey: ['projects'],
    queryFn: async () => (await api.get<List<Project>>('/api/v1/projects')).data,
    staleTime: 30_000,
  })
}

/** Retention is shown in the navigation tree because it is the setting most
 *  likely to be wrong and least likely to be looked at. */
export function retentionLabel(days: number | null): string {
  if (!days) return 'chưa đặt hạn lưu'
  if (days % 365 === 0) return `lưu ${days / 365} năm`
  if (days % 30 === 0) return `lưu ${days / 30} tháng`
  return `lưu ${days} ngày`
}
