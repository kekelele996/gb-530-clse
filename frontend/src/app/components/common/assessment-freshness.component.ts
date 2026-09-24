import { ChangeDetectionStrategy, Component, Input, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FreshnessReport } from '../../types/dose';

@Component({
  selector: 'app-assessment-freshness',
  standalone: true,
  imports: [CommonModule],
  template: `
    <section *ngIf="report" class="freshness" [class.stale]="!report.fresh" [class.fresh]="report.fresh"
             [attr.aria-label]="report.fresh ? 'Assessment snapshot is current' : 'Assessment snapshot is outdated'">
      <header>
        <span class="status-dot" aria-hidden="true"></span>
        <div>
          <strong>{{ report.fresh ? 'Snapshot matches current inputs' : 'Snapshot is outdated' }}</strong>
          <small>Freshness check {{ report.checked_at | date:'medium':'UTC' }} · evidence remains immutable</small>
        </div>
      </header>
      <dl class="dose-delta">
        <div><dt>Snapshot period dose</dt><dd>{{ report.snapshot_period_dose_msv | number:'1.3-3' }} mSv</dd></div>
        <div><dt>Current period dose</dt><dd>{{ report.current_period_dose_msv | number:'1.3-3' }} mSv</dd></div>
        <div [class.up]="report.period_dose_delta_msv > 0" [class.down]="report.period_dose_delta_msv < 0">
          <dt>Net period difference</dt><dd>{{ signed(report.period_dose_delta_msv) | number:'1.3-3' }} mSv</dd>
        </div>
      </dl>
      <ng-container *ngIf="!report.fresh">
        <p class="reason"><strong>Why it is blocked</strong> {{ report.stale_reason }}</p>
        <div *ngIf="report.exposure_changes.length" class="change-block">
          <h3>Verified exposure records changed ({{ report.exposure_changes.length }})</h3>
          <table>
            <thead><tr><th>Record</th><th>Source</th><th>Change</th><th>Dose impact</th></tr></thead>
            <tbody>
              <tr *ngFor="let change of report.exposure_changes">
                <td class="code">#{{ change.entry_id }}<br><small>{{ change.entry_type }}</small></td>
                <td>{{ change.source_ref || 'no longer in ledger' }}<br><small>{{ change.occurred_at | date:'mediumDate':'UTC' }}</small></td>
                <td><span class="kind" [attr.data-kind]="change.change_kind">{{ change.change_kind }}</span></td>
                <td [class.up]="change.dose_delta_msv > 0" [class.down]="change.dose_delta_msv < 0">{{ signed(change.dose_delta_msv) | number:'1.3-3' }} mSv</td>
              </tr>
            </tbody>
          </table>
          <p *ngIf="report.new_excluded_entry_count" class="excluded-note">
            {{ report.new_excluded_entry_count }} newer non-verified record(s) stay excluded from the dose total but are now visible in the ledger.
          </p>
        </div>
        <div *ngIf="report.worker_changes.length" class="change-block">
          <h3>Worker limits adjusted ({{ report.worker_changes.length }})</h3>
          <table>
            <thead><tr><th>Limit</th><th>Frozen in snapshot</th><th>Current value</th></tr></thead>
            <tbody>
              <tr *ngFor="let change of report.worker_changes">
                <td>{{ label(change.field) }}</td>
                <td>{{ change.snapshot_value | number:'1.3-3' }} mSv</td>
                <td class="up">{{ change.current_value | number:'1.3-3' }} mSv</td>
              </tr>
            </tbody>
          </table>
          <p class="version-note" *ngIf="report.current_worker_version !== report.snapshot_worker_version">
            Worker profile version moved v{{ report.snapshot_worker_version }} → v{{ report.current_worker_version }}.
          </p>
        </div>
        <footer>Acceptance is locked. Run a fresh reassessment; the superseded snapshot stays in the history and audit trail.</footer>
      </ng-container>
      <p *ngIf="report.fresh" class="ok-note">No verified records or worker limits changed since the snapshot; the RPO can act on this evidence.</p>
    </section>
  `,
  styles: [`
    .freshness { border: 1px solid var(--line); border-left-width: 4px; background: #fbfbf7; margin-top: 10px; }
    .freshness.fresh { border-left-color: #286858; }
    .freshness.stale { border-left-color: #b3402f; background: #fcf3f1; }
    header { display: flex; align-items: center; gap: 12px; padding: 13px 16px; }
    .status-dot { width: 10px; height: 10px; border-radius: 50%; flex: none; }
    .fresh .status-dot { background: #286858; } .stale .status-dot { background: #b3402f; }
    strong { display: block; font-size: 13px; } small { color: var(--muted); font-size: 10px; }
    .dose-delta { display: grid; grid-template-columns: repeat(3, 1fr); margin: 0 16px; border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); }
    .dose-delta div { padding: 10px 12px; border-right: 1px solid var(--line); }
    .dose-delta div:last-child { border-right: 0; }
    dt { color: var(--muted); font-size: 9px; text-transform: uppercase; } dd { margin: 3px 0 0; font-variant-numeric: tabular-nums; font-size: 14px; }
    .reason { margin: 12px 16px 0; font-size: 12px; line-height: 1.5; color: #6d2a20; }
    .reason strong { display: inline-block; margin-right: 6px; color: #8c2929; }
    .change-block { margin: 12px 16px; }
    h3 { margin: 0 0 6px; font-size: 11px; text-transform: uppercase; letter-spacing: .04em; color: #8c2929; }
    table { width: 100%; border-collapse: collapse; font-size: 11px; }
    th { text-align: left; color: var(--muted); font-weight: 600; padding: 4px 8px; border-bottom: 1px solid var(--line); }
    td { padding: 5px 8px; border-bottom: 1px solid #efe7e4; vertical-align: top; }
    .code { font-variant-numeric: tabular-nums; }
    .kind { padding: 1px 6px; border-radius: 2px; font-size: 9px; font-weight: 800; text-transform: uppercase; }
    .kind[data-kind="added"] { background: #f8dfde; color: #8c2929; }
    .kind[data-kind="removed"] { background: #e7ece7; color: #34413e; }
    .up { color: #8c2929; font-weight: 700; } .down { color: #185847; font-weight: 700; }
    .excluded-note, .version-note { margin: 8px 0 0; font-size: 10px; color: var(--muted); }
    footer { margin-top: 12px; padding: 9px 16px; background: #f8dfde; color: #6d2a20; font-size: 11px; border-top: 1px solid #e3b7b1; }
    .ok-note { margin: 10px 16px 14px; font-size: 11px; color: #185847; }
  `],
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AssessmentFreshnessComponent {
  private readonly reportSignal = signal<FreshnessReport | null>(null);
  @Input({ required: true }) set report(value: FreshnessReport | null | undefined) {
    this.reportSignal.set(value ?? null);
  }
  get report(): FreshnessReport | null { return this.reportSignal(); }

  signed(value: number): number { return value; }

  label(field: string): string {
    if (field === 'administrative_limit_msv') return 'Administrative limit';
    if (field === 'annual_limit_msv') return 'Annual legal limit';
    return field;
  }
}
