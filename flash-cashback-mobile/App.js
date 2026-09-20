import React, { useState, useCallback, useEffect } from 'react';
import {
  SafeAreaView,
  ScrollView,
  View,
  Text,
  TextInput,
  TouchableOpacity,
  ActivityIndicator,
  StyleSheet,
  RefreshControl,
  Platform,
} from 'react-native';
import { StatusBar } from 'expo-status-bar';
import { api, formatIDR } from './src/api';

// Default API base. On a physical device, replace "localhost" with your
// machine's LAN IP (e.g. http://192.168.1.10:8080). Editable in the app below.
const DEFAULT_BASE_URL =
  Platform.OS === 'android' ? 'http://10.0.2.2:8080' : 'http://localhost:8080';

export default function App() {
  const [baseUrl, setBaseUrl] = useState(DEFAULT_BASE_URL);
  const [userId, setUserId] = useState('demo-user');

  const [campaign, setCampaign] = useState(null);
  const [balance, setBalance] = useState(0);
  const [history, setHistory] = useState([]);

  const [paymentAmount, setPaymentAmount] = useState('50000');
  const [redeemAmount, setRedeemAmount] = useState('1000');

  const [loading, setLoading] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [notice, setNotice] = useState(null); // { type: 'ok'|'err', text }

  const refresh = useCallback(async () => {
    try {
      const [c, b, h] = await Promise.all([
        api.getCampaign(baseUrl),
        api.getBalance(baseUrl, userId),
        api.getHistory(baseUrl, userId),
      ]);
      setCampaign(c);
      setBalance(b.balance);
      setHistory(h.entries || []);
    } catch (e) {
      setNotice({ type: 'err', text: 'Load failed: ' + e.message });
    }
  }, [baseUrl, userId]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const onRefresh = async () => {
    setRefreshing(true);
    await refresh();
    setRefreshing(false);
  };

  const pay = async () => {
    setLoading(true);
    setNotice(null);
    try {
      const res = await api.makePayment(baseUrl, userId, parseInt(paymentAmount, 10));
      setNotice({
        type: 'ok',
        text: `Payment ok · earned ${formatIDR(res.cashback_awarded)} (${res.status})`,
      });
      await refresh();
    } catch (e) {
      setNotice({ type: 'err', text: e.message });
    } finally {
      setLoading(false);
    }
  };

  const redeem = async () => {
    setLoading(true);
    setNotice(null);
    try {
      const res = await api.redeem(baseUrl, userId, parseInt(redeemAmount, 10));
      setNotice({ type: 'ok', text: `Redeemed ${formatIDR(res.redeemed)}` });
      await refresh();
    } catch (e) {
      setNotice({ type: 'err', text: e.message });
    } finally {
      setLoading(false);
    }
  };

  const budgetPct = campaign
    ? Math.max(0, Math.min(100, (campaign.remaining / campaign.total_budget) * 100))
    : 0;

  return (
    <SafeAreaView style={styles.safe}>
      <StatusBar style="light" />
      <ScrollView
        contentContainerStyle={styles.container}
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} />}
      >
        <Text style={styles.title}>Flash Cashback</Text>

        {/* Balance card */}
        <View style={[styles.card, styles.balanceCard]}>
          <Text style={styles.balanceLabel}>Your cashback balance</Text>
          <Text style={styles.balanceValue}>{formatIDR(balance)}</Text>
        </View>

        {/* Campaign card */}
        <View style={styles.card}>
          <Text style={styles.sectionTitle}>Campaign budget</Text>
          {campaign ? (
            <>
              <View style={styles.progressTrack}>
                <View style={[styles.progressFill, { width: `${budgetPct}%` }]} />
              </View>
              <Text style={styles.muted}>
                {formatIDR(campaign.remaining)} of {formatIDR(campaign.total_budget)} left
                {campaign.active ? '' : ' · campaign ended'}
              </Text>
            </>
          ) : (
            <Text style={styles.muted}>—</Text>
          )}
        </View>

        {/* Notice */}
        {notice && (
          <View style={[styles.notice, notice.type === 'ok' ? styles.noticeOk : styles.noticeErr]}>
            <Text style={styles.noticeText}>{notice.text}</Text>
          </View>
        )}

        {/* Make a payment */}
        <View style={styles.card}>
          <Text style={styles.sectionTitle}>Make a payment</Text>
          <Text style={styles.hint}>Earn 5% back. Payments under Rp 20.000 earn nothing.</Text>
          <TextInput
            style={styles.input}
            keyboardType="number-pad"
            value={paymentAmount}
            onChangeText={setPaymentAmount}
            placeholder="Amount in IDR"
          />
          <PrimaryButton title="Pay" onPress={pay} disabled={loading} />
        </View>

        {/* Redeem */}
        <View style={styles.card}>
          <Text style={styles.sectionTitle}>Redeem cashback</Text>
          <TextInput
            style={styles.input}
            keyboardType="number-pad"
            value={redeemAmount}
            onChangeText={setRedeemAmount}
            placeholder="Amount to redeem"
          />
          <PrimaryButton title="Redeem" onPress={redeem} disabled={loading} variant="dark" />
        </View>

        {/* History */}
        <View style={styles.card}>
          <Text style={styles.sectionTitle}>Cashback history</Text>
          {history.length === 0 ? (
            <Text style={styles.muted}>No activity yet.</Text>
          ) : (
            history.map((e) => (
              <View key={e.id} style={styles.row}>
                <View>
                  <Text style={styles.rowType}>
                    {e.entry_type === 'EARN' ? 'Earned' : 'Redeemed'}
                  </Text>
                  <Text style={styles.muted}>
                    {new Date(e.created_at).toLocaleString()}
                  </Text>
                </View>
                <Text
                  style={[
                    styles.rowAmount,
                    { color: e.entry_type === 'EARN' ? '#0B7A4B' : '#B4232B' },
                  ]}
                >
                  {e.entry_type === 'EARN' ? '+' : '-'}
                  {formatIDR(e.amount)}
                </Text>
              </View>
            ))
          )}
        </View>

        {/* Settings */}
        <View style={styles.card}>
          <Text style={styles.sectionTitle}>Connection</Text>
          <Text style={styles.hint}>API base URL</Text>
          <TextInput style={styles.input} value={baseUrl} onChangeText={setBaseUrl} autoCapitalize="none" />
          <Text style={styles.hint}>User ID (X-User-Id)</Text>
          <TextInput style={styles.input} value={userId} onChangeText={setUserId} autoCapitalize="none" />
          <PrimaryButton title="Reload" onPress={refresh} variant="outline" />
        </View>

        {loading && <ActivityIndicator size="large" color="#0B7A4B" style={{ marginVertical: 16 }} />}
        <View style={{ height: 40 }} />
      </ScrollView>
    </SafeAreaView>
  );
}

function PrimaryButton({ title, onPress, disabled, variant = 'green' }) {
  const bg =
    variant === 'dark' ? '#12433A' : variant === 'outline' ? 'transparent' : '#0B7A4B';
  const color = variant === 'outline' ? '#0B7A4B' : '#fff';
  const border = variant === 'outline' ? { borderWidth: 1, borderColor: '#0B7A4B' } : null;
  return (
    <TouchableOpacity
      style={[styles.button, { backgroundColor: bg }, border, disabled && { opacity: 0.5 }]}
      onPress={onPress}
      disabled={disabled}
    >
      <Text style={[styles.buttonText, { color }]}>{title}</Text>
    </TouchableOpacity>
  );
}

const styles = StyleSheet.create({
  safe: { flex: 1, backgroundColor: '#F2F4F5' },
  container: { padding: 16 },
  title: { fontSize: 28, fontWeight: '800', color: '#12433A', marginBottom: 12 },
  card: {
    backgroundColor: '#fff',
    borderRadius: 14,
    padding: 16,
    marginBottom: 14,
    shadowColor: '#000',
    shadowOpacity: 0.06,
    shadowRadius: 8,
    shadowOffset: { width: 0, height: 2 },
    elevation: 2,
  },
  balanceCard: { backgroundColor: '#0B7A4B' },
  balanceLabel: { color: '#CDEBD9', fontSize: 14 },
  balanceValue: { color: '#fff', fontSize: 34, fontWeight: '800', marginTop: 4 },
  sectionTitle: { fontSize: 16, fontWeight: '700', color: '#12433A', marginBottom: 8 },
  hint: { color: '#6B7280', fontSize: 13, marginBottom: 6 },
  muted: { color: '#6B7280', fontSize: 13 },
  input: {
    borderWidth: 1,
    borderColor: '#D1D5DB',
    borderRadius: 10,
    paddingHorizontal: 12,
    paddingVertical: 10,
    fontSize: 16,
    marginBottom: 10,
    backgroundColor: '#fff',
  },
  button: { borderRadius: 10, paddingVertical: 13, alignItems: 'center', marginTop: 2 },
  buttonText: { fontSize: 16, fontWeight: '700' },
  progressTrack: {
    height: 12,
    backgroundColor: '#E5E7EB',
    borderRadius: 6,
    overflow: 'hidden',
    marginBottom: 8,
  },
  progressFill: { height: 12, backgroundColor: '#0B7A4B' },
  row: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingVertical: 10,
    borderTopWidth: 1,
    borderTopColor: '#F0F0F0',
  },
  rowType: { fontSize: 15, fontWeight: '600', color: '#12433A' },
  rowAmount: { fontSize: 16, fontWeight: '700' },
  notice: { borderRadius: 10, padding: 12, marginBottom: 14 },
  noticeOk: { backgroundColor: '#DCFCE7' },
  noticeErr: { backgroundColor: '#FEE2E2' },
  noticeText: { color: '#111827', fontSize: 14 },
});
